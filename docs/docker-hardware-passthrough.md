# F1 的 Docker 硬件直通

Ubuntu 和 Armbian 都预装 Docker 和 Compose v2。1Panel 由用户自行安装。先在 htoold 工作台绑定协调器，再通过 1Panel 管理 Zigbee2MQTT 编排。

## 绑定协调器

1. 从管理口访问 htoold 并以系统管理员身份登录。
2. 在「设备映射」查看候选串口。对照设备的序列号、USB 端口或板级接口，确认实际连接到 Zigbee 协调器，再点击「绑定设备」。
3. 等待「宿主机设备已就绪」。宿主机路径为 `/dev/zigbee_dongle`。协调器未连接、USB 序列号冲突和等待稳定路径时，页面会显示对应状态。候选列表过期后需要重新选择。若另一个页面已修改绑定，先刷新当前绑定再操作。若更换绑定时后台中断，重启后会恢复最后一次已提交的绑定。

F1 的 J22 UART7 标为 RS-485。UART4 和 UART9 标为通用串口，必须确认接线后才可选作 Zigbee。带序列号的 USB 设备按型号、序列号和接口识别。没有序列号时按物理端口识别，更换插口后需要重新绑定。固定别名使用当前核对过的 USB 物理路径，避免另一插口的重复序列号设备抢占全局 `by-id` 链接。

## 在 1Panel 编辑 Compose

在 1Panel 的容器编排里，将下方设备字段合并到现有的 Zigbee2MQTT 服务。保留项目原有的镜像、网络和持久卷设置。

```yaml
services:
  zigbee2mqtt:
    devices:
      - "/dev/zigbee_dongle:/dev/zigbee:rw"
```

Zigbee2MQTT 的 `configuration.yaml` 中设置容器内路径 `serial.port: /dev/zigbee`，`serial.adapter` 使用对应设备的协议。单个协调器交给 Zigbee2MQTT 独占，Home Assistant 通过 MQTT 接入。非 root 容器需通过 `group_add` 加入宿主机设备节点对应的数值 GID。可用 `stat -Lc '%g %A' /dev/zigbee_dongle` 核对设备权限。

## 拔插、更换和排查

- 设备拔出时，工作台标记为未连接并保留绑定。同一设备恢复后先让应用重连。更换协调器或应用重连失败时，在 1Panel 中重建 Zigbee2MQTT 服务，保留原持久目录和 Compose 配置。切换协调器前应备份 Zigbee 网络和配对数据。
- 如果映射无法启动，依次确认宿主机上的 `readlink -f /dev/zigbee_dongle` 指向正确设备、容器有 `/dev/zigbee`、应用使用了正确串口和协议。
- 若遇到身份冲突，移除重复设备后刷新列表。若发现同名路径被其他服务占用，通过宿主机确认路径来源后再处理。
- 设备未就绪时只影响使用它的容器。Docker、1Panel、Mosquitto 和 Home Assistant 可以独立运行。宿主机诊断可查看 `journalctl -u htool-hardware.service`。
