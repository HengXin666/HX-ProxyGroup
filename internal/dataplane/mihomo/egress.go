package mihomo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

const linuxRouteTablePath = "/proc/net/route"

// ResolveEgressInterface returns the physical interface that the managed
// Mihomo process should bind for outbound traffic. Reading /proc/net/route
// intentionally uses the main routing table, which is not replaced by Clash
// style policy routing through a TUN device.
func ResolveEgressInterface(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "off", "none", "disabled":
		return "", nil
	case "auto":
		file, err := os.Open(linuxRouteTablePath)
		if err != nil {
			return "", fmt.Errorf("open Linux main route table: %w", err)
		}
		defer file.Close()
		value, err = parseDefaultRouteInterface(file)
		if err != nil {
			return "", err
		}
	case "":
		return "", errors.New("egress interface is required")
	}

	device, err := net.InterfaceByName(value)
	if err != nil {
		return "", fmt.Errorf("resolve egress interface %q: %w", value, err)
	}
	if device.Flags&net.FlagUp == 0 {
		return "", fmt.Errorf("egress interface %q is down", value)
	}
	if device.Flags&net.FlagLoopback != 0 {
		return "", fmt.Errorf("egress interface %q is loopback", value)
	}
	return device.Name, nil
}

func parseDefaultRouteInterface(reader io.Reader) (string, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 1<<20))
	bestInterface := ""
	bestMetric := int(^uint(0) >> 1)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&1 == 0 {
			continue
		}
		metric, err := strconv.Atoi(fields[6])
		if err != nil || metric < 0 {
			continue
		}
		if bestInterface == "" || metric < bestMetric || metric == bestMetric && fields[0] < bestInterface {
			bestInterface = fields[0]
			bestMetric = metric
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Linux main route table: %w", err)
	}
	if bestInterface == "" {
		return "", errors.New("Linux main route table has no active default route")
	}
	return bestInterface, nil
}
