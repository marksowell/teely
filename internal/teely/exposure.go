package teely

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This is passive socket inspection: it never probes, adopts or stops a process.
func inspectListeners() (map[int][]PortConflict, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpcn")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0 {
		err = nil
	}
	return parseListeners(string(out)), err
}

func parseListeners(output string) map[int][]PortConflict {
	result := map[int][]PortConflict{}
	var current PortConflict
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			current = PortConflict{}
			current.PID, _ = strconv.Atoi(line[1:])
		case 'c':
			current.Command = line[1:]
		case 'n':
			current.Address = line[1:]
			i := strings.LastIndex(current.Address, ":")
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(current.Address[i+1:])
			if err == nil {
				result[port] = append(result[port], current)
			}
		}
	}
	return result
}

func listenerExposure(listeners []PortConflict, err error) (string, string, bool) {
	if err != nil {
		return "Exposure unknown", "Socket inspection failed; Teely cannot verify this port's bind address.", false
	}
	if len(listeners) == 0 {
		return "", "", false
	}
	var details []string
	exposed, unknown := false, false
	for _, l := range listeners {
		details = append(details, l.Command+" (pid "+strconv.Itoa(l.PID)+") "+l.Address)
		i := strings.LastIndex(l.Address, ":")
		if i < 0 {
			unknown = true
			continue
		}
		host := strings.Trim(l.Address[:i], "[]")
		if host == "*" {
			exposed = true
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			unknown = true
		} else if !ip.IsLoopback() {
			exposed = true
		}
	}
	if exposed {
		return "Direct network listener", strings.Join(details, "; ") + ". This app port can bypass Teely's LAN password. Bind the app to localhost to prevent direct network access.", true
	}
	if unknown {
		return "Exposure unknown", strings.Join(details, "; "), false
	}
	return "Loopback only", strings.Join(details, "; ") + ". Observed listeners on the app port are local to this Mac.", false
}

func expectedAppExposure(app AppConfig) (string, string, bool) {
	file, err := os.Open(filepath.Join(app.WorkingDir, "package.json"))
	if err != nil {
		return "", "", false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024))
	if err != nil {
		return "", "", false
	}
	return expectedCommandExposure(app.Command, projectSnapshot{Files: map[string]string{"package.json": string(data)}})
}

func expectedCommandExposure(command string, snapshot projectSnapshot) (string, string, bool) {
	index, flag := loopbackFramework(command, snapshot)
	if flag == "" {
		return "", "", false
	}
	fields := strings.Fields(command)
	script := parsePackageJSON(snapshot.Files["package.json"]).Scripts[fields[index]]
	args := append(strings.Fields(script), fields[index+1:]...)
	host := ""
	for i := 0; i < len(args); i++ {
		arg, value, equals := strings.Cut(args[i], "=")
		if arg != flag && !(flag == "--hostname" && arg == "-H") {
			continue
		}
		if !equals {
			value = "0.0.0.0" // Vite's bare --host requests all interfaces.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				value = args[i]
			}
		}
		host = value
	}
	if host == "" {
		return "", "", false // Do not guess defaults that config files can override.
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return "Loopback expected", "The startup command requests " + host + ". This is an estimate, not an observed listener; Teely checks again after startup.", false
	}
	if ip != nil {
		return "Network listener expected", "The startup command requests " + host + ", which may allow direct LAN access without Teely's password. This is an estimate; Teely checks the actual listener after startup.", true
	}
	return "", "", false
}
