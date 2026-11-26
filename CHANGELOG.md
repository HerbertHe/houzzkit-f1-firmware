# 变更记录

## 未发布

### 新增

- 添加 Armbian Noble ARM64 固件及独立构建入口。Armbian 和 Ubuntu 复用 F1 Rockchip BSP、USB 直刷 eMMC 的整包格式和发布校验。
- 添加 `htool` 网页首启。Ubuntu 和 Armbian 均可通过管理口的 `https://houzzkit.local:80` 或实际 IP 完成两步设置。保留 `192.168.55.1` 供直连。先设置 `root` 密码，再创建普通 SSH 用户。无需配对码。
- 使用持久记录恢复未完成的账号设置。成功后锁定首次注册，网页提供普通用户的 SSH 指引。

### 变更

- 两种固件在设备上生成独立 SSH 主机密钥。用户用普通账号通过 SSH 登录，需要管理权限时可执行 `su -`。
- Armbian 首次账号设置由网页完成。现有管理口和 HDMI 终端继续用于连接与维护。
- 共享工作流交叉编译 Linux ARM64 的 `htool`，并在 ext4 制作前把服务装入两种固件。
