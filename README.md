# Houzzkit Force1 固件

面向 HOUZZkit Force1（蓝精灵、Rockchip RK3568）的固件仓库。Ubuntu 24.04 和 Armbian Noble 使用同一套 F1 Rockchip BSP 和 USB 整包刷写流程。两种系统都以 eMMC 为启动和根存储，Armbian 方案支持后续将 Docker 数据迁到 NVMe。

- [下载 GitHub Releases](https://github.com/HerbertHe/houzzkit-f1-firmware/releases)
- [查看 GitHub Actions](https://github.com/HerbertHe/houzzkit-f1-firmware/actions/workflows/firmware.yml)
- [Armbian 可执行适配方案](docs/armbian-adaptation.md)
- [变更记录](CHANGELOG.md)

| 路径 | 用途 |
| --- | --- |
| [houzzkit-f1-bsp-k6199](houzzkit-f1-bsp-k6199) | Rockchip BSP、U-Boot、Linux 6.1 内核、F1 设备树及刷机工具 |
| [houzzkit-f1-rootfs-u2404](houzzkit-f1-rootfs-u2404) | Ubuntu 根文件系统快照、元数据和镜像生成脚本 |
| [Ubuntu 工作流](.github/workflows/firmware.yml) | 构建 Ubuntu 24.04 固件 |
| [Armbian 工作流](.github/workflows/firmware-armbian.yml) | 构建 Armbian Noble 固件 |
| [Armbian 根文件系统适配](houzzkit-f1-rootfs-armbian) | 固定上游版本、配置板级用户态和首次启动 |
| [可复用构建工作流](.github/workflows/firmware-build.yml) | 准备依赖、编译 BSP 与 Ubuntu rootfs、打包并上传固件 |

## 下载前先区分镜像

| 文件 | 使用方法 |
| --- | --- |
| `houzzkit-firmware-ubuntu_24_04-YYYY_MM_DD[_N].img.zst` | 解压为 `.img` 后使用 Windows RKDevTool 或 macOS RKDevTool 图形界面的整包升级功能 |
| 同名 `.img.zst.part-000`、`.part-001` 等 | 核验每个分卷，按编号合并，再解压 |
| 单独的 `rootfs.img`、`boot.img`、`recovery.img` | BSP 分区镜像，仅用于有对应分区写入说明的维护流程 |
| `houzzkit-firmware-armbian_<版本>-YYYY_MM_DD[_N].img.zst` | 使用同一 USB OTG 整包方式直刷 eMMC。选取已完成真机验证的 F1 Release。 |

两种固件的 Release 解压后得到的是 **Rockchip 整包**。不要用 `dd`、磁盘镜像写入器或 `rkdeveloptool wl 0` 将这个文件写入 eMMC。

## 刷机前准备

1. 从同一个 Release 下载所有镜像文件和同名 `.sha256`。需要稳定版本时选正式 Release。
2. 备份设备上的系统和应用数据。整包升级可能重写分区及清除数据。保证设备独立供电，并用能传数据的 USB 线将 F1 的 OTG 口接到电脑。板级说明标注为下方 USB 口。
3. 进入 Rockchip 模式。现有系统可启动时，按住 **RECOVERY**，点按 **RESET**，等待约 5 秒再松开 RECOVERY，进入 **LOADER**。空盘或无法启动时，按住板上的 **MASKROM** 键，点按 RESET，再松开 MASKROM。
4. 工具出现多个 Rockchip 设备时先核对或断开其他设备。刷写过程中保持电源和 USB 连接。

按键、OTG 接口位置和 Windows 界面截图见 [HOUZZkit 原版固件烧录指南](https://yinbaiyuan.github.io/firmware/firmware.html#%E5%9B%BA%E4%BB%B6%E7%83%A7%E5%BD%95)。

## Windows 刷机（RKDevTool on Windows）

建议使用 Windows x64，仓库内的 RockUSB 驱动归档提供 x64 和 x86 驱动。下载 [DriverAssitant v5.12](houzzkit-f1-bsp-k6199/tools/windows/DriverAssitant_v5.12.zip) 和 [RKDevTool v3.19](houzzkit-f1-bsp-k6199/tools/windows/RKDevTool_Release_v3.19.zip)，分别解压。原作者的截图使用 v2.86，本仓库提供 v3.19。以管理员身份运行 `DriverInstall.exe` 安装驱动，并准备 [官方 Zstandard Windows 命令行程序](https://github.com/facebook/zstd/releases)，将 `zstd.exe` 放在下载目录或加入 PATH。

在由下载文件组成的目录中打开 PowerShell，把示例名称替换成实际 Release 的完整名称。下面的脚本会先核验校验文件中列出的每个资产，再按编号合并分卷并解压。已有同名结果时请先移走旧文件。

```powershell
$base = 'houzzkit-firmware-ubuntu_24_04-2026_11_26'
$archive = "$base.img.zst"
$zstd = if (Test-Path -LiteralPath '.\zstd.exe') { '.\zstd.exe' } else { 'zstd.exe' }
$ErrorActionPreference = 'Stop'

$verified = @(Get-Content -LiteralPath "$base.sha256" | ForEach-Object {
    if ($_ -notmatch '^([0-9a-fA-F]{64})\s+\*?(.+)$') {
        throw "无效的校验记录：$_"
    }
    $expected, $name = $Matches[1], $Matches[2]
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $name).Hash
    if ($actual -ne $expected) { throw "校验失败：$name" }
    $name
})
if ($verified.Count -eq 0) { throw '校验文件为空' }

$parts = @($verified | Where-Object { $_ -like "$archive.part-*" } | Sort-Object |
    ForEach-Object { Get-Item -LiteralPath $_ })
if ($parts.Count -gt 0) {
    $output = [IO.File]::Open((Join-Path $PWD.Path $archive), [IO.FileMode]::CreateNew)
    try {
        foreach ($part in $parts) {
            $inputFile = $part.OpenRead()
            try { $inputFile.CopyTo($output) } finally { $inputFile.Dispose() }
        }
    } finally {
        $output.Dispose()
    }
} elseif ($archive -notin $verified) {
    throw '校验文件中没有要解压的镜像'
}
& $zstd -d $archive -o "$base.img"
if ($LASTEXITCODE -ne 0) { throw '固件解压失败' }
```

1. 打开 `RKDevTool.exe`，按上面的步骤将 F1 切到刷机模式。状态栏应显示已发现一个 LOADER 或 MASKROM 设备。若没有识别，在设备管理器确认设备使用 RockUSB 驱动，然后重新连接。
2. 切到 **升级固件（Upgrade Firmware）** 页，点击 **固件（Firmware）**，选择解压后的 `.img`。原版教程中的 `FULL` 是原厂文件名标识。本仓库的 `houzzkit-firmware-ubuntu_24_04-….img` 已是整包，可以直接选中。
3. 等待固件信息显示后点击 **升级（Upgrade）**。以工具日志报告完成为准，等待设备自动重启。

## macOS 刷机

先使用 Homebrew 安装 [zstd](https://formulae.brew.sh/formula/zstd)，将 Release 固件核验并解压为 `.img`。这一步适用于图形界面和命令行两种刷写方式。

```sh
brew install zstd
```

在下载目录中核验并解压，先把 `base` 改成实际 Release 名。

```sh
base='houzzkit-firmware-ubuntu_24_04-2026_11_26'
shasum -a 256 -c "$base.sha256"
```

只有校验全部通过时才继续。如果下载的是分卷，先执行合并命令。编号从 `000` 开始并固定宽度，通配符按文件名顺序读取。已有同名合并结果时移走旧文件再执行。

```sh
# 仅在下载的是分卷时执行这一行
( set -C; LC_ALL=C; export LC_ALL; cat "$base.img.zst".part-* > "$base.img.zst" )
zstd -d "$base.img.zst" -o "$base.img"
```

### 图形界面（第三方 RKDevTool）

1. 从 [hiifong/rkdevtool Releases](https://github.com/hiifong/rkdevtool/releases) 下载正式发布的 macOS Universal `.dmg`，安装到“应用程序”。正式包面向 Intel 与 Apple Silicon。
2. 用 OTG 数据线接入 F1，按前述步骤进入 LOADER 或 MASKROM。在界面中确认已选中 F1。
3. 打开 **升级固件（Upgrade Firmware）**，选择刚解压的 Rockchip 整包 `.img`，执行升级。等待进度和日志确认完成，再等待设备重启。
4. 第三方项目文档声明支持 Rockchip `update.img` 整包写入。本仓库尚未完成 F1 真机验证；若工具无法识别镜像或写入失败，可用下方 Rockchip 命令行工具或 Windows RKDevTool。 [第三方 RKDevTool 文档](https://github.com/hiifong/rkdevtool#readme)

### 命令行（Rockchip Upgrade Tool）

下载并完整解压 BSP 自带的 [Rockchip Upgrade Tool v2.13 for macOS](houzzkit-f1-bsp-k6199/tools/mac/upgrade_tool/upgrade_tool_v2.13_for_mac.zip)，让 `upgrade_tool` 与 `config.ini` 保持在一起。确认识别到一台 Rockchip 设备后再执行整包升级。把示例中的工具目录和镜像路径改成实值。

```sh
cd "$HOME/Downloads/upgrade_tool_v2.13_for_mac"
chmod u+x ./upgrade_tool
./upgrade_tool ld
sudo ./upgrade_tool uf "$HOME/Downloads/实际固件名称.img"
```

如果枚举不到设备，先确认 F1 进入了正确模式、USB 线支持传输数据，并尝试直连电脑。若 `uf` 报告包格式或 loader 不兼容，保留错误输出，改用上面的 Windows 整包升级路径。

## 首次启动与构建

### 网页首启

Ubuntu 和 Armbian 共用 [htool](htool) 首启向导，无需接 HDMI。

1. 将 F1 靠近 HDMI 的管理口接入可信的局域网，电脑接入同一网络。打开 `https://houzzkit.local`。如果网络不支持 mDNS，可在路由器中查到管理口的 DHCP 地址，再用该 IP 加 `:443` 访问。
2. 没有 DHCP 时，可网线直连。将电脑的直连网口设为 `192.168.55.2/24`，网关留空，再打开 `https://192.168.55.1:443`。设备生成自签名 HTTPS 证书，在确认设备身份后处理浏览器提示。
3. 在第一步为 `root` 设置管理员密码，第二步创建普通用户。完成页会给出 SSH 命令。用普通用户的密码登录，需要管理权限时执行 `su -` 并输入管理员密码。

HTTP 80 会跳转到 HTTPS 443，密码仅通过 HTTPS 提交。首启页面只接受管理口连接，无配对码，首个成功提交创建账号。注册完成后会锁定，重启后继续保持。两种固件均禁止 `root` 直接通过 SSH 登录。 [首启排查与恢复](docs/htool-first-boot.md)

初始化完成后，SSH 可以使用完成页提供的主机名或 IP。运行 `findmnt -no SOURCE,FSTYPE /` 可核对根分区位于 eMMC。Ubuntu 版本读 `/etc/os-release`；Armbian 的版本与板名读 `/etc/armbian-release`。Armbian 的 NVMe 数据迁移与 Compose 部署见 [适配方案](docs/armbian-adaptation.md)。

### GitHub Actions

- **Ubuntu** 使用仓库中的 Ubuntu 24.04 ARM64 根文件树。**Armbian** 则由固定版本的官方 Build Framework 生成 Noble ARM64 Minimal 用户态，然后加入 F1 的固件文件、首启配置和相匹配的 BSP 内核模块。
- 共享构建使用 F1 BSP 生成 boot、Recovery、U-Boot、分区表和 Rockchip `update.img`。两种固件共享包清单、镜像头、压缩校验和发布逻辑。
- 推送到 `main` 或更新目标为 `main` 的 PR 时，某发行版的 rootfs、构建脚本或专属工作流改动触发对应构建。共享 BSP、htool 和共享工作流改动触发两种构建。只改根 README、`docs/` 或版权文件会跳过，`main` 也可手动触发。
- `main` 构建成功后发布正式版，同仓库 PR 发布预发行版，Fork PR 可下载 Actions 构建产物。两种固件分别按北京时间编号。以 Armbian 为例，当天第一个是 `houzzkit-firmware-armbian_26.11.0-trunk.140-2026_11_26`，第二个加 `_1`。

## 许可证

**Copyright (C) 2026 Herbert He <Herbert.He0229@gmail.com>**

Herbert He 在本项目中的原创贡献按 [GNU GPL v3](LICENSE) 发布，另有文件级声明的从其声明。第三方 BSP、Linux、U-Boot、Ubuntu 软件包和工具保留各自的版权与许可。见 [版权声明](COPYRIGHT)。
