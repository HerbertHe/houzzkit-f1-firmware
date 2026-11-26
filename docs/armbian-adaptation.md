# Houzzkit Force1 Armbian 固件适配方案

状态为实施与验收基线。当前仓库的 GitHub Actions 仍构建 Rockchip Ubuntu 24.04 整包。Armbian 镜像、F1 安装适配和 SSD 迁移功能须完成以下步骤后再交付。

## 1. 目标系统

- 设备为 Houzzkit Force1，RK3568、4 GB 内存、板载 32 GB eMMC，支持可选 M.2 NVMe SSD。
- 首版采用 Armbian Ubuntu 24.04 LTS ARM64 Minimal。eMMC 承载 bootloader、`/boot` 和 ext4 根文件系统 `/`。采用 Armbian 安装器的 `emmc` 模式。
- 无 SSD 时系统可独立启动，Docker 可使用 eMMC。启用可选迁移后，Docker 引擎数据、应用持久数据、数据库和容器日志均置于 SSD。
- 默认运行 `multi-user.target`，保留 HDMI TTY、串口诊断、SSH、双以太网口和 Avahi。局域网主机名使用 `houzzkit.local`，HTTP 面板按实际端口访问。镜像不安装桌面、显示管理器、Xorg 或 Wayland。
- 四个核心应用使用 Compose v2。Zigbee2MQTT 独占 Zigbee 协调器，Home Assistant 通过 MQTT 接入。1Panel 提供辅助管理入口。

## 2. 阶段一：建立 F1 板级构建配置

### 固定构建输入

在 Linux CI 上固定 Armbian Build Framework 提交、发行版 Noble、F1 BSP 提交和工具链版本。使用项目自有的 Armbian 板定义、补丁和镜像配置管理改动。板定义选用项目标识 `houzzkit-force1`；实现板定义和 BSP 引用后，再运行以下构建命令。所选内核分支以板定义中实际注册的 `BRANCH` 为准。

```bash
./compile.sh build BOARD=houzzkit-force1 BRANCH=vendor RELEASE=noble \
  BUILD_MINIMAL=yes BUILD_DESKTOP=no KERNEL_CONFIGURE=no
```

首版沿用可在 F1 上验证的 Rockchip BSP 6.1 和 U-Boot 组合，建立 Armbian 内的板族适配或对应构建钩子。生成内核、DTB、initramfs、模块和板级固件包时固定来源版本。使用同一次内核构建的模块，不从旧 Ubuntu 快照直接复制 `.ko`。记录镜像的构建提交和所有输入版本。若上游 Armbian 后续提供经过验证的 F1 支持，再评估切换内核和 U-Boot。 [Armbian 板定义](https://docs.armbian.com/build-framework/adding-a-board/) · [Armbian 构建命令](https://docs.armbian.com/build-framework/commands/)

### 硬件验证基线

| 部件 | 适配工作 | 实机验证 |
| --- | --- | --- |
| eMMC / microSD | 对照现有板级 DTS 和板载启动位置，确认 microSD 可引导，安装器能识别 eMMC，U-Boot 可从 eMMC 加载系统 | `lsblk`、串口启动日志、拔掉 microSD 后冷启动 |
| PCIe / NVMe | 保留 regulator、复位 GPIO、PHY、pinctrl 和 NVMe 驱动。实测 M.2 插槽对应的控制器及链路，不以 DTS 节点名推断插槽速率 | `lspci -nn`、`lsblk`、`nvme list`、重启和冷启动 |
| 双以太网 | 保留两路 GMAC、PHY、时钟与复位。以接口持久标识配置网络 | `ip -br link`、`ethtool`，两个端口分别连通 |
| HDMI | 保留 Rockchip DRM、fbcon 和 `console=tty0`，启用 `getty@tty1.service`。串口控制台使用板上实测参数 | 接 HDMI 后出现 TTY 并可登录，默认目标为 `multi-user.target` |
| Zigbee / 蓝牙 | 在现有系统采集设备枚举，确认实际芯片、USB VID/PID 或 UART、协调器协议和蓝牙固件。保留实际所需 UART/USB 串口驱动、固件和 hci 初始化 | 查看 `lsusb`、`/dev/serial/by-id`、`dmesg`、`bluetoothctl list`；板载 UART 按实测建立稳定设备名 |

保留 ext4、overlayfs、namespace、内存 cgroup、容器联网与 `json-file` 运行所需的内核支持。ZRAM 和 LZ4 必须以目标镜像的实际内核配置和运行结果验收。相关板级源文件见 [F1 DTS](../houzzkit-f1-bsp-k6199/kernel-6.1/arch/arm64/boot/dts/rockchip/rk3568-jl-rm01.dtsi) 和 [F1 内核配置](../houzzkit-f1-bsp-k6199/kernel-6.1/arch/arm64/configs/jl_v1_linux_defconfig)。

## 3. 阶段二：安装到 eMMC

1. 先备份原厂镜像、设备数据和分区布局，确认能够按 [README 的 Rockchip 恢复路径](../README.md#刷机前准备)重新进入 LOADER 或 MASKROM。安装到 eMMC 会覆盖原系统。
2. 将通过板级验证的 **F1 专用 Armbian microSD 磁盘镜像**写入 microSD。Windows 和 macOS 可用支持原始磁盘镜像的写卡工具。核对目标是 microSD，再开机测试 SSH、HDMI 和存储枚举。
3. 在 F1 上用 `lsblk -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINTS,MODEL` 核对当前启动介质和 eMMC。执行 `sudo armbian-install`，选择 eMMC 目标、`emmc` 模式与 ext4。若未出现支持的 eMMC 选项，回到板级适配检查，按键与刷机工具见根 README。
4. 关机取出 microSD，在不插 SSD 的情况下冷启动。用 `findmnt -no SOURCE,FSTYPE /`、`findmnt -T /boot`、`lsblk -f` 确认系统与引导文件来自 eMMC。若 `/boot` 位于根分区而非独立分区，`findmnt -T /boot` 应显示同一 eMMC 文件系统。

eMMC 使用其余可用空间承载系统，首版不额外划分 `/emmc_storage`。系统启用 SSH，首次启动创建管理凭据，设备生成独立 SSH 主机密钥。禁止在镜像中写入通用登录密码。将 **至少 8 GiB 根分区可用空间**作为部署 SSD 模式后的容量验收目标。 [Armbian eMMC 安装](https://docs.armbian.com/getting-started/install-to-internal-storage/)

## 4. 阶段三：可选 SSD 数据盘与迁移

| 模式 | Docker 与应用数据 | SSD 故障行为 |
| --- | --- | --- |
| 默认 eMMC | Docker `/var/lib/docker`。应用绑定目录统一放在 `/var/lib/houzzkit/apps`，或使用受 Compose 管理的卷 | SSD 无需存在 |
| 已启用 SSD | ext4 数据盘按 UUID 挂载到 `/mnt/ssd`。Docker 目录 `/mnt/ssd/docker`，应用目录 `/mnt/ssd/apps`。如使用 containerd image store，持久目录使用 `/mnt/ssd/containerd` | 系统仍可从 eMMC 启动，SSH、TTY 和 Avahi 保持可用，容器暂停 |

`/etc/fstab` 中数据盘记录的目标形态如下。用 `lsblk -f` 或 `blkid` 确认真实 UUID。默认镜像不预填设备专属 UUID。

```fstab
UUID=<SSD_EXT4_UUID> /mnt/ssd ext4 defaults,nofail,x-systemd.device-timeout=10s 0 2
```

在 SSD 模式为 Docker、Docker socket、依赖 SSD 的 containerd 和 Compose unit 增加挂载依赖及重复可执行的前置检查。检查要确认 `/mnt/ssd` 本身是挂载点、来源 UUID 正确、文件系统为 ext4 且可写，并且目标目录位于该挂载之下。挂载失败时拒绝启动容器服务，也不在根分区创建替代数据目录。测试可使用 `findmnt --mountpoint /mnt/ssd -no UUID,FSTYPE,OPTIONS` 检查匹配项。

迁移按以下顺序实施：

1. 记录 `docker info` 的存储后端及 Docker Root Dir、`docker volume ls`、`docker inspect` 的实际挂载。备份 Docker、containerd、Compose 配置和全部应用数据。
2. 核对 SSD 型号、UUID、容量及现有文件。优先复用已有 ext4 数据盘。仅在确认选中空盘或已备份的数据盘后，初始化或格式化。通过挂载测试后再停服务。
3. 停止 Compose 栈、Docker socket、Docker daemon 和相关 containerd 服务，确保 SQLite 等数据库停止写入。用保留属主、ACL、xattr 与硬链接的方式复制实际目录，例如离线树复制采用 `rsync -aHAX --numeric-ids`。分别处理 Docker 存储和应用 bind mount。
4. 写入新路径和挂载依赖。保持 Docker 存储后端及版本一致，切换后核对镜像、卷、数据库和 Zigbee 配对记录。Compose bind mount 使用显式源路径，并避免自动创建不存在的宿主数据目录。
5. SSD 写入前可通过旧配置恢复。切换后如果已有业务写入，先停服务，将最新数据迁回再切换。完成冷启动、缺盘和回退测试后，另行确认是否清理旧副本。

Docker `data-root` 只影响引擎自身的数据。使用 containerd image store 时，还要配置 containerd 的存储目录。 [Docker daemon 目录](https://docs.docker.com/engine/daemon/)

## 5. 阶段四：系统与应用配置

### ZRAM、日志与服务

- 保留 `armbian-zram-config`，显式配置物理内存约 50% 的逻辑交换容量，目标约 2 GiB，算法 LZ4。设置 `vm.swappiness=10`，禁用 eMMC 和 SSD 的磁盘 swap。用 `zramctl`、`swapon --show`、`sysctl vm.swappiness` 验证真实生效值。
- 根据实机 `MemTotal` 给宿主预留约 800 MiB。四个预置容器硬上限合计 2176 MiB。烧机期间记录 ZRAM 占用、容器内存、SSH 响应和 OOM 事件；容器上限不包含新的第三方面板应用。
- Docker daemon 设置 `log-driver: json-file`、`max-size: "10m"`、`max-file: "3"`。更新日志配置后重新创建需要继承新配置的容器。
- 用 Compose v2 单独管理核心容器。每个服务设置服务层级的 `mem_limit`，再用 `docker inspect` 核对运行态的内存限制。正式镜像固定经过验证的 ARM64 版本或镜像摘要。 [Armbian 内存配置](https://docs.armbian.com/config/storage/) · [Docker 日志配置](https://docs.docker.com/engine/logging/drivers/json-file/) · [Compose 服务配置](https://docs.docker.com/reference/compose-file/services/)

### 应用配置

| 服务 | 镜像候选及硬上限 | 配置与数据 |
| --- | --- | --- |
| Home Assistant | `ghcr.io/home-assistant/home-assistant`，1536 MiB | 默认 bridge，显式发布局域网端口。应用目录 `ha/config`。Recorder 设置 `commit_interval: 30`、`auto_purge: true`、`auto_repack: true`，给出按实际实体填写的 `exclude.entity_globs` 示例。Bridge 内使用 MQTT，其他依赖组播发现的集成单独验收。 |
| Mosquitto | `eclipse-mosquitto`，128 MiB | 用户名、密码和 ACL 由首次部署生成，持久化配置和数据。默认只在应用 bridge 内向 HA 和 Z2M 提供 MQTT，不启用 WebSocket。 |
| Zigbee2MQTT | `ghcr.io/koenkk/zigbee2mqtt`，256 MiB | 应用目录 `zigbee2mqtt/data`。用稳定设备节点映射独占协调器，设备协议按实际芯片配置，开启 HA 的 MQTT discovery。 |
| 1Panel | `moelin/1panel` 是待验证候选，256 MiB | 核对来源、ARM64 镜像、持久化目录和兼容性才纳入发行。仅发布局域网管理端口，不启用 `privileged`。如需完整 Docker 管理，授权宿主 socket 等同于授予宿主管理能力，需在部署时显式启用。核心栈仍由独立 Compose 项目编排。 |

单个协调器由 Z2M 使用。HA 的直通需求若改为启用 ZHA，则需第二个独立协调器。默认配置不启用非必要的高频轮询。局域网访问通过 Avahi 的 `houzzkit.local` 配合应用实际端口，按需增加 `_http._tcp` 服务公告。 [Zigbee2MQTT 协调器限制](https://www.zigbee2mqtt.io/guide/faq/) · [HA Recorder](https://www.home-assistant.io/integrations/recorder/) · [Docker 安全模型](https://docs.docker.com/engine/security/)

## 6. CI、发布与验收

Armbian 使用独立 Linux 工作流，固定构建输入，PR 产生测试产物，`main` 构建候选镜像，通过真机验收后再标为稳定版。交付物包括 F1 专用 microSD 镜像、SHA-256、构建版本清单、安装说明和 Compose 版本。Armbian 镜像使用例如 `houzzkit-firmware-armbian-noble-YYYY_MM_DD.img.zst` 的独立名称。Linux、DTB、U-Boot 和模块升级要作为配套版本验证，保留原厂恢复路径。所用第三方软件包含的许可与源码提供义务，按组件单独核对。

| 顺序 | 操作 | 判定 |
| --- | --- | --- |
| 1 | 不装 SSD，从 eMMC 冷启动 | 根挂载在 eMMC，HDMI TTY、SSH、Avahi 双网口连通 |
| 2 | 关机安装 SSD，再验证两次冷启动 | NVMe 稳定识别，根分区仍位于 eMMC |
| 3 | 迁移 Docker 和应用数据 | `docker info` 的 Docker Root Dir、containerd 数据、卷、应用数据库与容器日志均位于 SSD |
| 4 | 关机取下已配置的 SSD，再启动 | 主系统和 SSH 正常，Docker 与核心栈暂停，未在 eMMC 上生成误用的数据目录 |
| 5 | 恢复 SSD 并重启容器服务 | 使用原 UUID，恢复 HA 历史、Zigbee 配对和 MQTT 数据 |
| 6 | 运行至少 30 分钟，同时进行 Zigbee、MQTT、HA 历史写入和面板状态查询 | `docker stats` 与容器配置的内存上限一致，无宿主或容器 OOM，SSH 保持可用 |
| 7 | 验证升级和恢复 | eMMC 更新后可启动，SSD 应用数据完整，Rockchip 恢复入口可用 |

验收归档最少包含镜像及校验值、源提交、F1 板卡版本、测试 NVMe 型号与链路、Zigbee 协调器型号、四个镜像摘要，以及启动和故障测试记录。
