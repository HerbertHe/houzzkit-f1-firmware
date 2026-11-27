package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const coordinatorAlias = "/dev/zigbee_dongle"

type deviceIdentity struct {
	Kind      string `json:"kind"`
	Board     string `json:"board,omitempty"`
	Vendor    string `json:"vendor,omitempty"`
	Product   string `json:"product,omitempty"`
	Interface string `json:"interface,omitempty"`
	Serial    string `json:"serial,omitempty"`
	Topology  string `json:"topology,omitempty"`
}

type serialCandidate struct {
	ID        string         `json:"id"`
	Path      string         `json:"path"`
	Label     string         `json:"label"`
	Identity  deviceIdentity `json:"identity"`
	Available bool           `json:"available"`
	links     []string
}

var serialName = regexp.MustCompile(`^tty(S|USB|ACM)[0-9]+$`)

func textFile(path string, limit int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	return strings.TrimSpace(string(buf[:n]))
}

func udevTTY(path string) (map[string]string, []string) {
	number := textFile(filepath.Join(path, "dev"), 40)
	properties := make(map[string]string)
	var links []string
	if number == "" {
		return properties, links
	}
	for _, line := range strings.Split(textFile("/run/udev/data/c"+number, 8192), "\n") {
		switch {
		case strings.HasPrefix(line, "E:"):
			key, value, ok := strings.Cut(line[2:], "=")
			if ok && len(value) <= 160 {
				properties[key] = value
			}
		case strings.HasPrefix(line, "S:serial/") && len(line) <= 200:
			link := filepath.Clean("/dev/" + line[2:])
			if strings.HasPrefix(link, "/dev/serial/") {
				links = append(links, link)
			}
		}
	}
	return properties, links
}

func boardSerialLabel(board, topology, name string) (string, bool) {
	if name == "ttyS2" {
		return "调试串口", false
	}
	if strings.Contains(board, "rockchip,rk3568-rzw03") {
		switch filepath.Base(topology) {
		case "serial@fe680000":
			return "J22 · UART4 通用串口", true
		case "serial@fe6b0000":
			return "J22 · UART7 RS-485", false
		case "serial@fe6d0000":
			return "J22 · UART9 通用串口", true
		default:
			return "板级 UART · 用途未确认", false
		}
	}
	return "板级 UART · 板型未验证", false
}

func serialInventory() ([]serialCandidate, error) {
	entries, err := os.ReadDir("/sys/class/tty")
	if err != nil {
		return nil, err
	}
	board := strings.Join(strings.FieldsFunc(textFile("/proc/device-tree/compatible", 512),
		func(r rune) bool { return r == 0 }), ",")
	consoles := strings.Fields(textFile("/sys/class/tty/console/active", 512))
	candidates := make([]serialCandidate, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !serialName.MatchString(name) {
			continue
		}
		node := "/dev/" + name
		info, err := os.Stat(node)
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			continue
		}
		sys := filepath.Join("/sys/class/tty", name)
		if !sameDeviceNumber(info, textFile(filepath.Join(sys, "dev"), 40)) {
			continue
		}
		hardwarePath, err := filepath.EvalSymlinks(sys)
		if err != nil {
			continue
		}
		c := serialCandidate{Path: node, Available: true}
		if strings.HasPrefix(name, "ttyS") {
			controller, err := filepath.EvalSymlinks(filepath.Join(sys, "device", "of_node"))
			if err != nil {
				continue
			}
			c.Identity = deviceIdentity{Kind: "uart", Board: board, Topology: controller}
			c.Label, c.Available = boardSerialLabel(board, controller, name)
		} else {
			props, links := udevTTY(sys)
			if props["ID_BUS"] != "usb" || props["ID_VENDOR_ID"] == "" || props["ID_MODEL_ID"] == "" {
				continue
			}
			iface := props["ID_USB_INTERFACE_NUM"]
			// Multiport bridges can expose several serial ports on one interface.
			if port := textFile(filepath.Join(sys, "device", "port_number"), 32); port != "" {
				iface += ":" + port
			}
			c.Identity = deviceIdentity{
				Kind: "usb", Vendor: props["ID_VENDOR_ID"], Product: props["ID_MODEL_ID"],
				Interface: iface, Serial: props["ID_SERIAL_SHORT"],
			}
			if c.Identity.Serial == "" {
				c.Identity.Topology = props["ID_PATH"]
				c.Available = c.Identity.Topology != ""
			}
			c.Label = strings.TrimSpace(strings.ReplaceAll(props["ID_VENDOR_FROM_DATABASE"]+" "+props["ID_MODEL"], "_", " "))
			if c.Label == "" {
				c.Label = "USB 串口"
			}
			c.links = links
			c.Available = c.Available && c.target() != ""
		}
		for _, console := range consoles {
			if console == name {
				c.Available = false
				c.Label += " · 系统控制台"
			}
		}
		c.ID = fmt.Sprintf("%x", sha256.Sum256([]byte(hardwarePath+"\x00"+node)))
		candidates = append(candidates, c)
	}
	return candidates, nil
}

func (c serialCandidate) target() string {
	// Keep the link tied to udev identity when the USB driver provides a stable link.
	prefix := "/dev/serial/by-path/"
	if c.Identity.Serial != "" {
		prefix = "/dev/serial/by-id/"
	}
	if c.Identity.Kind == "usb" {
		for _, link := range c.links {
			resolved, err := filepath.EvalSymlinks(link)
			if err == nil && strings.HasPrefix(link, prefix) && resolved == c.Path {
				return link
			}
		}
		return ""
	}
	return c.Path
}
