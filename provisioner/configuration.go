package provisioner

import (
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"

	"github.com/digineo/goldflags"
	"github.com/digineo/ubnt-tools/discovery"
	pssh "github.com/digineo/ubnt-tools/provisioner/ssh"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v2"
)

// ExampleYAML provides a complete set of config options in YAML format
// and can be used for documentation purposes.
const ExampleYAML = `	---
	# Notes on path values (config_directory, firmware_directory, web.templates):
	#
	# - relative paths (i.e. those starting with "./") will be resolved relative
	#   to the directory of this config file
	# - a "~/" prefix resolves to the home directory of the user running the
	#   provisioner

	# Where do we find device specific system.cfg files? These files must be
	# named "aabbccddeeff.cfg", where aabbccddeeff is the MAC address of the
	# device (i.e. lowercase, without seperator).
	config_directory: /tmp/ubnt-config/configs

	# Where do we find the firmware images?
	firmware_directory: /tmp/ubnt-config/firmwares

	# This mapping describes safe upgrade paths. As key use the basename of the
	# firmware image (located in the firmware_directory) and as values provide
	# a list of firmware version identifiers found in the wild.
	safe_upgrade_paths:
	  "XC.v7.2.4.31259.160714.1715.bin":
	    - "XC.qca955x.v7.2.1.30741.160412.1342"

	# This must be a list of interface names with broadcast and multicast
	# capabilities.
	interfaces:
	- eth0

	# When accessing the devices via SSH, the authentication methods declared
	# here are tried in order. This sample lists all available types:
	ssh:
	- type: keyfile
	  path: ~/.ssh/id_rsa
	  password: foobar     # required if keyfile is password protected
	- type: password
	  password: super-secret
	- type: ssh-agent      # try ssh-agent (needs SSH_AUTH_SOCK env var)

	web:
	  # The internal webserver will bind to this address. You really should not
	  # use a publicly accessible IP address.
	  host: 127.0.0.1
	  port: 8080
`

type sshAuthMethod struct {
	Type     string `yaml:"type"`
	Password string `yaml:"password"`
	Path     string `yaml:"path"`
}

// Configuration maps config options to values
type Configuration struct {
	ConfigDirectory     string              `yaml:"config_directory"`
	FirmwareDirectory   string              `yaml:"firmware_directory"`
	SafeUpgradePaths    map[string][]string `yaml:"safe_upgrade_paths"`
	reverseUpgradePaths map[string]string   // inferred from SafeUpgradePaths
	InterfaceNames      []string            `yaml:"interfaces"`

	SSHAuthMethods []sshAuthMethod `yaml:"ssh"`
	sshAuthMethods []ssh.AuthMethod

	Web struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"web"`

	autoDiscoverer *discovery.Discover
	devices        *deviceCache
}

// LoadConfig reads a YAML file and converts it to a config object
func LoadConfig(fileName string) (c *Configuration, errs []error) {
	c = &Configuration{}

	file, err := ioutil.ReadFile(fileName)
	if err != nil {
		errs = append(errs, err)
		return
	}
	if err := yaml.Unmarshal(file, &c); err != nil {
		errs = append(errs, err)
		return
	}

	// the following errors are recoverable

	base := filepath.Dir(fileName)
	if dirErrs := checkDirectory("config_directory", base, &c.ConfigDirectory); len(dirErrs) > 0 {
		errs = append(errs, dirErrs...)
	}
	if dirErrs := checkDirectory("firmware_directory", base, &c.FirmwareDirectory); len(dirErrs) > 0 {
		errs = append(errs, dirErrs...)
	}

	c.reverseUpgradePaths = make(map[string]string)
	for target, sources := range c.SafeUpgradePaths {
		for _, source := range sources {
			if _, exists := c.reverseUpgradePaths[source]; exists {
				errs = append(errs, fmt.Errorf("multiple upgrade paths for %s detected", source))
				continue
			}
			c.reverseUpgradePaths[source] = target
		}
	}

	if len(c.InterfaceNames) == 0 {
		errs = append(errs, fmt.Errorf("missing option interfaces, at least one name (or '*') must be given"))
	}

	if c.Web.Port <= 0 || c.Web.Port > math.MaxUint16 {
		errs = append(errs, fmt.Errorf("config option web.port out of range"))
	}

	c.sshAuthMethods = make([]ssh.AuthMethod, 0, len(c.SSHAuthMethods))
	for _, m := range c.SSHAuthMethods {
		switch m.Type {
		case "", "password": // Type=="" is an alias for password
			if m.Password != "" {
				c.sshAuthMethods = append(c.sshAuthMethods, ssh.Password(m.Password))
			}
		case "ssh-agent":
			if a := pssh.Agent(); a != nil {
				c.sshAuthMethods = append(c.sshAuthMethods, a)
			}
		case "keyfile":
			if key, ok := pssh.ReadPrivateKey(m.Path, m.Password); ok {
				c.sshAuthMethods = append(c.sshAuthMethods, key)
			}
		default:
			errs = append(errs, fmt.Errorf("unknown auth method %q", m.Type))
		}
	}

	if len(errs) == 0 {
		c.devices = &deviceCache{
			list: make(map[string]*Device),
		}
	}

	return
}

func checkDirectory(name string, baseDir string, dir *string) (errs []error) {
	deref := *dir
	if deref == "" {
		errs = append(errs, fmt.Errorf("missing %s config option", name))
		return
	}

	if expanded, err := goldflags.ExpandPath(deref, baseDir); err == nil {
		deref = expanded
	} else {
		errs = append(errs, err)
		return
	}

	if _, err := os.Stat(deref); err != nil {
		if os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("directory %s (%s) doesn't exist", name, deref))
		} else {
			errs = append(errs, err)
		}
	}

	*dir = deref
	return
}

// FirmwareImages prepares a list of firmware names
func (c *Configuration) FirmwareImages() []string {
	return goldflags.ReadDir(c.FirmwareDirectory)
}

// DeviceConfigs prepares a list of device configuration names
func (c *Configuration) DeviceConfigs() []string {
	return goldflags.ReadDir(c.ConfigDirectory)
}
