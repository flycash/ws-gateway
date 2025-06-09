package compression

import "github.com/gobwas/ws/wsflate"

// Config 压缩配置
type Config struct {
	Enabled         bool `yaml:"enabled"`
	ServerMaxWindow int  `yaml:"serverMaxWindow"`
	ClientMaxWindow int  `yaml:"clientMaxWindow"`
	ServerNoContext bool `yaml:"serverNoContext"`
	ClientNoContext bool `yaml:"clientNoContext"`
	Level           int  `yaml:"level"`
}

// ToParameters 将配置转换为wsflate参数
func (c *Config) ToParameters() wsflate.Parameters {
	return wsflate.Parameters{
		ServerMaxWindowBits:     wsflate.WindowBits(c.ServerMaxWindow),
		ClientMaxWindowBits:     wsflate.WindowBits(c.ClientMaxWindow),
		ServerNoContextTakeover: c.ServerNoContext,
		ClientNoContextTakeover: c.ClientNoContext,
	}
}

// State 压缩状态，包含协商后的扩展信息
type State struct {
	Enabled    bool
	Extension  *wsflate.Extension
	Parameters wsflate.Parameters
}
