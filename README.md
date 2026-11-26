# Houzzkit Force1 固件

面向 HOUZZkit Force1（蓝精灵、Rockchip RK3568）的固件仓库。现有 GitHub Actions 使用 Rockchip BSP 和 Ubuntu 24.04 ARM64 根文件系统构建 Rockchip 整包。Armbian 是独立的适配计划，默认系统安装到 eMMC，Docker 数据可以扩展到 NVMe SSD。

- [下载 GitHub Releases](https://github.com/HerbertHe/houzzkit-f1-firmware/releases)
- [查看 GitHub Actions](https://github.com/HerbertHe/houzzkit-f1-firmware/actions/workflows/firmware.yml)
- [Armbian 可执行适配方案](docs/armbian-adaptation.md)

| 路径 | 用途 |
| --- | --- |
| [houzzkit-f1-bsp-k6199](houzzkit-f1-bsp-k6199) | Rockchip BSP、U-Boot、Linux 6.1 内核、F1 设备树及刷机工具 |
| [houzzkit-f1-rootfs-u2404](houzzkit-f1-rootfs-u2404) | Ubuntu 根文件系统快照、元数据和镜像生成脚本 |
| [GitHub Actions 工作流](.github/workflows/firmware.yml) | 构建 Ubuntu 刷机包、计算校验值并发布 |

## 下载前先区分镜像

| 文件 | 使用方法 |
| --- | --- |
| `houzzkit-firmware-ubuntu_24_04-YYYY_MM_DD[_N].img.zst` | 解压为 `.img` 后使用 Windows RKDevTool 或 macOS Rockchip `upgrade_tool` 的整包升级功能 |
| 同名 `.img.zst.part-000`、`.part-001` 等 | 核验每个分卷，按编号合并，再解压 |
| 单独的 `rootfs.img`、`boot.img`、`recovery.img` | BSP 分区镜像，仅用于有对应分区写入说明的维护流程 |
| 将来发布的 F1 专用 Armbian microSD 镜像 | 按 Armbian 方案写入 microSD，启动后再安装到 eMMC |

Ubuntu Release 解压后得到的是 **Rockchip 整包**。不要用 `dd`、磁盘镜像写入器或 `rkdeveloptool wl 0` 将这个文件写入 eMMC。

## 刷机前准备

1. 从同一个 Release 下载所有镜像文件和同名 `.sha256`。需要稳定版本时选正式 Release。
2. 备份设备上的系统和应用数据。整包升级可能重写分区及清除数据。保证设备独立供电，并用能传数据的 USB 线将 F1 的 OTG 口接到电脑。板级说明标注为下方 USB 口。
3. 进入 Rockchip 模式。现有系统可启动时，按住 **RECOVERY**，点按 **RESET**，再松开 RECOVERY，进入 **LOADER**。空盘或无法启动时，按住板上的 **MASKROM** 键，点按 RESET，再松开 MASKROM。
4. 工具出现多个 Rockchip 设备时先核对或断开其他设备。刷写过程中保持电源和 USB 连接。

按键位置见 [BSP 刷机说明](houzzkit-f1-bsp-k6199/README.md)。

## Windows 刷机

建议使用 Windows x64，仓库内的 RockUSB 驱动归档提供 x64 和 x86 驱动。下载 [DriverAssitant v5.12](houzzkit-f1-bsp-k6199/tools/windows/DriverAssitant_v5.12.zip) 和 [RKDevTool v3.19](houzzkit-f1-bsp-k6199/tools/windows/RKDevTool_Release_v3.19.zip)，分别解压。以管理员身份运行 `DriverInstall.exe` 安装驱动，并准备 [官方 Zstandard Windows 命令行程序](https://github.com/facebook/zstd/releases)，将 `zstd.exe` 放在下载目录或加入 PATH。

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

1. 打开 `RKDevTool.exe`，确认状态栏显示一个 LOADER 或 MASKROM 设备。
2. 打开 **升级固件** 页，点击 **固件**，选择解压后的 `.img`。
3. 等待工具识别固件，再点击 **升级**。看到升级完成后等待设备重启。

## macOS 刷机

仓库自带的 [Rockchip Upgrade Tool v2.13 for macOS](houzzkit-f1-bsp-k6199/tools/mac/upgrade_tool/upgrade_tool_v2.13_for_mac.zip) 包含 `arm64` 和 `x86_64` 两种架构。解压整个工具目录，让 `upgrade_tool` 与 `config.ini` 保持在一起。使用 Homebrew 安装 [zstd](https://formulae.brew.sh/formula/zstd)。

```sh
brew install zstd
```

在下载目录中核验并解压，先把 `base` 改成实际 Release 名。示例中的 `$HOME` 是 macOS 当前用户的主目录。

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

将 F1 切到 LOADER 或 MASKROM 模式。进入工具解压目录，确认恰好识别到一台 Rockchip 设备，再执行整包升级。把示例中的工具目录和镜像路径改成实值。

```sh
cd "$HOME/Downloads/upgrade_tool_v2.13_for_mac"
chmod u+x ./upgrade_tool
./upgrade_tool ld
sudo ./upgrade_tool uf "$HOME/Downloads/实际固件名称.img"
```

如果枚举不到设备，先确认 F1 进入了正确模式、USB 线支持传输数据，并尝试直连电脑。若 `uf` 报告包格式或 loader 不兼容，保留错误输出，改用上面的 Windows 整包升级路径。

## 首次启动与构建

首次开机先从 HDMI 查看启动状态，或在路由器中查看分配的地址。现有 Ubuntu rootfs 将 `eno1` 配置为 `192.168.55.1/24`。若使用该口直连，可给电脑设置同网段的其他地址。局域网支持 mDNS 时也可尝试 `houzzkit.local`。Ubuntu 镜像预装 SSH 和 Docker，局域网主机名解析与 HTTP 面板是两个独立功能。

构建流程在 GitHub Actions 的 Linux runner 上运行。`main` 推送触发正式发布，同仓库 PR 发布预发行版，Fork PR 可下载 Actions 构建产物。回看每个产物对应的提交、构建日志与校验文件，请使用该 Release 的说明页。

## 许可证

**Copyright (C) 2026 Herbert He <Herbert.He0229@gmail.com>**

Herbert He 在本项目中的原创贡献按 [GNU GPL v3](LICENSE) 发布，另有文件级声明的从其声明。第三方 BSP、Linux、U-Boot、Ubuntu 软件包和工具保留各自的版权与许可。见 [版权声明](COPYRIGHT)。
