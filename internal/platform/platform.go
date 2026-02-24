package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type Platform string

const (
	PlatformDarwin Platform = "darwin"
	PlatformLinux  Platform = "linux"
)

type Paths struct {
	ConfigDir    string
	RoutesFile   string
	CertsDir     string
	ResolverPath string // OS-specific path that tells the system to use our DNS
}

func Detect() Platform {
	switch runtime.GOOS {
	case "darwin":
		return PlatformDarwin
	case "linux":
		return PlatformLinux
	default:
		panic("unsupported platform: " + runtime.GOOS)
	}
}

func GetPaths(p Platform) Paths {
	configDir := filepath.Join(os.Getenv("HOME"), ".config", "pmux")

	var resolverPath string
	switch p {
	case PlatformDarwin:
		resolverPath = "/etc/resolver/test"
	case PlatformLinux:
		resolverPath = "/etc/systemd/resolved.conf.d/pmux.conf"
	}

	return Paths{
		ConfigDir:    configDir,
		RoutesFile:   filepath.Join(configDir, "routes.json"),
		CertsDir:     filepath.Join(configDir, "certs"),
		ResolverPath: resolverPath,
	}
}

// ResolverConfigured checks if the OS DNS resolver is pointed at our DNS server.
func ResolverConfigured(p Platform, paths Paths) bool {
	_, err := os.Stat(paths.ResolverPath)
	return err == nil
}

// ConfigureResolver sets up the OS to send .test queries to 127.0.0.1.
// This requires sudo and will prompt the user for their password.
func ConfigureResolver(p Platform, paths Paths) error {
	fmt.Println("pmux needs to configure DNS so .test domains resolve locally.")
	fmt.Println("This is a one-time setup that requires your password.")
	fmt.Println()

	switch p {
	case PlatformDarwin:
		return configureDarwin(paths)
	case PlatformLinux:
		return configureLinux(paths)
	}
	return nil
}

// RemoveResolver removes the DNS resolver configuration.
func RemoveResolver(p Platform, paths Paths) error {
	switch p {
	case PlatformDarwin:
		return exec.Command("sudo", "rm", "-f", paths.ResolverPath).Run()
	case PlatformLinux:
		if err := exec.Command("sudo", "rm", "-f", paths.ResolverPath).Run(); err != nil {
			return err
		}
		return exec.Command("sudo", "systemctl", "restart", "systemd-resolved").Run()
	}
	return nil
}

func configureDarwin(paths Paths) error {
	cmd := exec.Command("sudo", "sh", "-c",
		fmt.Sprintf("mkdir -p %s && echo 'nameserver 127.0.0.1' > %s",
			filepath.Dir(paths.ResolverPath), paths.ResolverPath))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CATrusted checks if the pmux CA cert is trusted by the OS trust store.
func CATrusted(p Platform, caCertPath string) bool {
	if _, err := os.Stat(caCertPath); os.IsNotExist(err) {
		return false // no cert yet, not trusted
	}

	switch p {
	case PlatformDarwin:
		// security verify-cert returns 0 if the cert is trusted
		err := exec.Command("security", "verify-cert", "-c", caCertPath, "-p", "ssl").Run()
		return err == nil
	case PlatformLinux:
		// Check if our CA is in the system trust store
		_, err := os.Stat("/usr/local/share/ca-certificates/pmux-ca.crt")
		return err == nil
	}
	return false
}

// TrustCA installs the pmux CA cert into the OS trust store.
// This requires sudo and will prompt the user for their password.
func TrustCA(p Platform, caCertPath string) error {
	fmt.Println("pmux needs to trust its CA certificate so browsers accept HTTPS on .test domains.")
	fmt.Println("This is a one-time setup that requires your password.")
	fmt.Println()

	switch p {
	case PlatformDarwin:
		cmd := exec.Command("sudo", "security", "add-trusted-cert",
			"-d", "-r", "trustRoot",
			"-k", "/Library/Keychains/System.keychain",
			caCertPath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case PlatformLinux:
		cmd := exec.Command("sudo", "sh", "-c",
			fmt.Sprintf("cp %s /usr/local/share/ca-certificates/pmux-ca.crt && update-ca-certificates",
				caCertPath))
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	return nil
}

func configureLinux(paths Paths) error {
	conf := `[Resolve]
DNS=127.0.0.1
Domains=~test`

	cmd := exec.Command("sudo", "sh", "-c",
		fmt.Sprintf("mkdir -p %s && echo '%s' > %s && systemctl restart systemd-resolved",
			filepath.Dir(paths.ResolverPath), conf, paths.ResolverPath))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
