package hostagent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/system/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/protocol"
)

type Backend interface {
	Inspect(context.Context) (protocol.Inspection, error)
	Services(context.Context, []string) ([]protocol.Service, error)
	Logs(context.Context, string, int) (string, error)
	RestartService(context.Context, string) (string, error)
	RestartRuntime(context.Context) (string, error)
}

type SystemBackend struct {
	cfg config.Host
}

func NewSystemBackend(cfg config.Host) *SystemBackend {
	return &SystemBackend{cfg: cfg}
}

func (b *SystemBackend) Inspect(_ context.Context) (protocol.Inspection, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return protocol.Inspection{}, err
	}
	result := protocol.Inspection{Hostname: hostname, OS: runtime.GOOS, Architecture: runtime.GOARCH}
	if value, err := readFirstFloat("/proc/uptime"); err == nil {
		result.UptimeSeconds = value
	}
	if value, err := readFirstFloat("/proc/loadavg"); err == nil {
		result.Load1 = value
	}
	if values, err := readMemory(); err == nil {
		result.MemoryTotal = values["MemTotal"] * 1024
		result.MemoryFree = values["MemAvailable"] * 1024
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		result.RootTotal = uint64(stat.Blocks) * uint64(stat.Bsize)
		result.RootFree = uint64(stat.Bavail) * uint64(stat.Bsize)
	}
	return result, nil
}

func (b *SystemBackend) Services(ctx context.Context, names []string) ([]protocol.Service, error) {
	services := make([]protocol.Service, 0, len(names))
	for _, name := range names {
		commandCtx, cancel := context.WithTimeout(ctx, b.cfg.CommandTimeout)
		output, err := exec.CommandContext(commandCtx, "systemctl", "show", name, "--property=ActiveState", "--property=SubState", "--value").Output()
		cancel()
		state := protocol.Service{Name: name, Active: "unavailable"}
		if err == nil {
			parts := strings.Split(strings.TrimSpace(string(output)), "\n")
			if len(parts) > 0 && parts[0] != "" {
				state.Active = parts[0]
			}
			if len(parts) > 1 {
				state.Sub = parts[1]
			}
		}
		services = append(services, state)
	}
	return services, nil
}

func (b *SystemBackend) Logs(ctx context.Context, service string, lines int) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, b.cfg.CommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, "journalctl", "--no-pager", "--unit", service, "--lines", strconv.Itoa(lines), "--output", "short-iso").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("journalctl failed: %w", err)
	}
	return string(output), nil
}

func (b *SystemBackend) RestartService(ctx context.Context, service string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, b.cfg.CommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, "systemctl", "restart", service).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("service restart failed: %w", err)
	}
	return string(output), nil
}

func (b *SystemBackend) RestartRuntime(ctx context.Context) (string, error) {
	script := b.cfg.RuntimeRestartScript
	if script == "" {
		return "", fmt.Errorf("runtime restart is not configured")
	}
	info, err := os.Lstat(script)
	if err != nil {
		return "", fmt.Errorf("inspect runtime restart script: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("runtime restart script must be a regular file not writable by group or others")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
		return "", fmt.Errorf("runtime restart script must be owned by root")
	}
	commandCtx, cancel := context.WithTimeout(ctx, b.cfg.CommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("runtime restart failed: %w", err)
	}
	return string(output), nil
}

func readFirstFloat(path string) (float64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty %s", path)
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readMemory() (map[string]uint64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]uint64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value
		}
	}
	return values, scanner.Err()
}
