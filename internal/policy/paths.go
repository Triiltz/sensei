package policy

import (
	"path"
	"strings"
)

// sensitiveDirs are path components whose contents are credentials or
// private configuration. Their output would be sent to the LLM provider.
var sensitiveDirs = []string{".ssh", ".aws", ".config", ".kube", ".gnupg", ".docker", ".azure", ".password-store"}

// sensitiveNames are file names (exact) holding credentials.
var sensitiveNames = []string{
	".netrc", ".npmrc", ".pypirc", ".pgpass", ".git-credentials", ".my.cnf",
	"shadow", "gshadow", "sudoers", "environ",
}

// sensitiveFragments flag any base name that contains them.
var sensitiveFragments = []string{"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "key", "secret", "credential", "token", "passw"}

// sensitiveSuffixes flag private keys and keystores.
var sensitiveSuffixes = []string{".pem", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", "_history"}

// sensitivePath reports whether p looks like it holds secrets: .env files,
// key material, credential stores, cloud and cluster config, shell history.
func sensitivePath(p string) bool {
	p = strings.ToLower(strings.Trim(p, `"'`))
	if p == "" {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		for _, d := range sensitiveDirs {
			if part == d {
				return true
			}
		}
	}

	base := path.Base(p)
	if strings.HasPrefix(base, ".env") || base == ".history" {
		return true
	}
	for _, n := range sensitiveNames {
		if base == n {
			return true
		}
	}
	for _, f := range sensitiveFragments {
		if strings.Contains(base, f) {
			return true
		}
	}
	for _, s := range sensitiveSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}

// hiddenGlob reports whether a glob could match dotfiles (like .* or ~/.c*),
// which is where secrets usually live.
func hiddenGlob(w word) bool {
	if !w.glob {
		return false
	}
	for _, part := range strings.Split(w.text, "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

// systemPrefixes are directories whose modification can break the machine.
var systemPrefixes = []string{"/etc", "/usr", "/boot", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/sys", "/proc", "/dev", "/var/lib", "/root", "/opt", "/srv", "/System", "/Library"}

// harmlessDevices may always be written to.
var harmlessDevices = []string{"/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty"}

// systemPath reports whether writing to p could damage the system.
func systemPath(p string) bool {
	p = strings.Trim(p, `"'`)
	if !strings.HasPrefix(p, "/") {
		return false
	}
	clean := path.Clean(p)
	if clean == "/" {
		return true
	}
	for _, d := range harmlessDevices {
		if clean == d {
			return false
		}
	}
	for _, prefix := range systemPrefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// startupFile reports whether p is a shell or login startup file, where a
// write would persist across sessions.
func startupFile(p string) bool {
	base := path.Base(strings.Trim(p, `"'`))
	switch base {
	case ".bashrc", ".bash_profile", ".bash_login", ".profile", ".zshrc", ".zshenv", ".zprofile", ".zlogin", "authorized_keys", "crontab":
		return true
	}
	return false
}

// rawDevice reports whether p is a device other than the harmless ones;
// reading a disk device would send raw disk contents to the provider.
func rawDevice(p string) bool {
	clean := path.Clean(strings.Trim(p, `"'`))
	if !strings.HasPrefix(clean, "/dev/") {
		return false
	}
	switch clean {
	case "/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return false
	}
	return true
}

func isDevNull(p string) bool {
	return path.Clean(strings.Trim(p, `"'`)) == "/dev/null"
}
