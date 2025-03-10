package config

import (
	"fmt"
	"os"
	"time"

	"github.com/mitchellh/go-homedir"
	"github.com/pion/webrtc/v3"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

var DefaultStunServers = []string{
	"stun.l.google.com:19302",
	"stun1.l.google.com:19302",
}

type CongestionControlProbeMode string

const (
	CongestionControlProbeModePadding CongestionControlProbeMode = "padding"
	CongestionControlProbeModeMedia   CongestionControlProbeMode = "media"
)

type Config struct {
	Port           uint32             `yaml:"port"`
	PrometheusPort uint32             `yaml:"prometheus_port,omitempty"`
	RTC            RTCConfig          `yaml:"rtc,omitempty"`
	Redis          RedisConfig        `yaml:"redis,omitempty"`
	Audio          AudioConfig        `yaml:"audio,omitempty"`
	Room           RoomConfig         `yaml:"room,omitempty"`
	TURN           TURNConfig         `yaml:"turn,omitempty"`
	WebHook        WebHookConfig      `yaml:"webhook,omitempty"`
	NodeSelector   NodeSelectorConfig `yaml:"node_selector,omitempty"`
	KeyFile        string             `yaml:"key_file,omitempty"`
	Keys           map[string]string  `yaml:"keys,omitempty"`
	Region         string             `yaml:"region,omitempty"`
	// LogLevel is deprecated
	LogLevel string        `yaml:"log_level,omitempty"`
	Logging  LoggingConfig `yaml:"logging,omitempty"`
	Limit    LimitConfig   `yaml:"limit,omitempty"`

	Development bool `yaml:"development,omitempty"`
}

type RTCConfig struct {
	UDPPort           uint32       `yaml:"udp_port,omitempty"`
	TCPPort           uint32       `yaml:"tcp_port,omitempty"`
	ICEPortRangeStart uint32       `yaml:"port_range_start,omitempty"`
	ICEPortRangeEnd   uint32       `yaml:"port_range_end,omitempty"`
	NodeIP            string       `yaml:"node_ip,omitempty"`
	STUNServers       []string     `yaml:"stun_servers,omitempty"`
	TURNServers       []TURNServer `yaml:"turn_servers,omitempty"`
	UseExternalIP     bool         `yaml:"use_external_ip"`
	UseICELite        bool         `yaml:"use_ice_lite,omitempty"`

	// Number of packets to buffer for NACK
	PacketBufferSize int `yaml:"packet_buffer_size,omitempty"`

	// Max bitrate for REMB
	MaxBitrate uint64 `yaml:"max_bitrate,omitempty"`

	// Throttle periods for pli/fir rtcp packets
	PLIThrottle PLIThrottleConfig `yaml:"pli_throttle,omitempty"`

	CongestionControl CongestionControlConfig `yaml:"congestion_control,omitempty"`

	// for testing, disable UDP
	ForceTCP bool `yaml:"force_tcp,omitempty"`
}

type TURNServer struct {
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	Protocol   string `yaml:"protocol"`
	Username   string `yaml:"username,omitempty"`
	Credential string `yaml:"credential,omitempty"`
}

type PLIThrottleConfig struct {
	LowQuality  time.Duration `yaml:"low_quality,omitempty"`
	MidQuality  time.Duration `yaml:"mid_quality,omitempty"`
	HighQuality time.Duration `yaml:"high_quality,omitempty"`
}

type CongestionControlConfig struct {
	Enabled        bool                       `yaml:"enabled"`
	AllowPause     bool                       `yaml:"allow_pause"`
	UseSendSideBWE bool                       `yaml:"send_side_bandwidth_estimation,omitempty"`
	ProbeMode      CongestionControlProbeMode `yaml:"padding_mode,omitempty"`
}

type AudioConfig struct {
	// minimum level to be considered active, 0-127, where 0 is loudest
	ActiveLevel uint8 `yaml:"active_level"`
	// percentile to measure, a participant is considered active if it has exceeded the ActiveLevel more than
	// MinPercentile% of the time
	MinPercentile uint8 `yaml:"min_percentile"`
	// interval to update clients, in ms
	UpdateInterval uint32 `yaml:"update_interval"`
	// smoothing for audioLevel values sent to the client.
	// audioLevel will be an average of `smooth_intervals`, 0 to disable
	SmoothIntervals uint32 `yaml:"smooth_intervals"`
}

type RedisConfig struct {
	Address  string `yaml:"address"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	UseTLS   bool   `yaml:"use_tls"`
}

type RoomConfig struct {
	// enable rooms to be automatically created
	AutoCreate         bool        `yaml:"auto_create"`
	EnabledCodecs      []CodecSpec `yaml:"enabled_codecs"`
	MaxParticipants    uint32      `yaml:"max_participants"`
	EmptyTimeout       uint32      `yaml:"empty_timeout"`
	EnableRemoteUnmute bool        `yaml:"enable_remote_unmute"`
}

type CodecSpec struct {
	Mime     string `yaml:"mime"`
	FmtpLine string `yaml:"fmtp_line"`
}

type LoggingConfig struct {
	JSON      bool   `yaml:"json"`
	Level     string `yaml:"level"`
	Sample    bool   `yaml:"sample,omitempty"`
	PionLevel string `yaml:"pion_level,omitempty"`
}

type TURNConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Domain      string `yaml:"domain"`
	CertFile    string `yaml:"cert_file"`
	KeyFile     string `yaml:"key_file"`
	TLSPort     int    `yaml:"tls_port"`
	UDPPort     int    `yaml:"udp_port"`
	ExternalTLS bool   `yaml:"external_tls"`
}

type WebHookConfig struct {
	URLs []string `yaml:"urls"`
	// key to use for webhook
	APIKey string `yaml:"api_key"`
}

type NodeSelectorConfig struct {
	Kind         string         `yaml:"kind"`
	SysloadLimit float32        `yaml:"sysload_limit"`
	Regions      []RegionConfig `yaml:"regions"`
}

// RegionConfig lists available regions and their latitude/longitude, so the selector would prefer
// regions that are closer
type RegionConfig struct {
	Name string  `yaml:"name"`
	Lat  float64 `yaml:"lat"`
	Lon  float64 `yaml:"lon"`
}

type LimitConfig struct {
	NumTracks   int32   `yaml:"num_tracks"`
	BytesPerSec float32 `yaml:"bytes_per_sec"`
}

func NewConfig(confString string, c *cli.Context) (*Config, error) {
	// start with defaults
	conf := &Config{
		Port: 7880,
		RTC: RTCConfig{
			UseExternalIP:     false,
			TCPPort:           7881,
			UDPPort:           0,
			ICEPortRangeStart: 0,
			ICEPortRangeEnd:   0,
			STUNServers:       []string{},
			MaxBitrate:        10 * 1024 * 1024, // 10 mbps
			PacketBufferSize:  500,
			PLIThrottle: PLIThrottleConfig{
				LowQuality:  500 * time.Millisecond,
				MidQuality:  time.Second,
				HighQuality: time.Second,
			},
			CongestionControl: CongestionControlConfig{
				Enabled:    true,
				AllowPause: true,
				ProbeMode:  CongestionControlProbeModePadding,
			},
		},
		Audio: AudioConfig{
			ActiveLevel:     35, // -35dBov
			MinPercentile:   40,
			UpdateInterval:  400,
			SmoothIntervals: 2,
		},
		Redis: RedisConfig{},
		Room: RoomConfig{
			AutoCreate: true,
			// by default only enable opus and VP8
			EnabledCodecs: []CodecSpec{
				{Mime: webrtc.MimeTypeOpus},
				{Mime: webrtc.MimeTypeVP8},
				{Mime: webrtc.MimeTypeH264},
				// {Mime: webrtc.MimeTypeVP9},
			},
			EmptyTimeout: 5 * 60,
		},
		Logging: LoggingConfig{
			PionLevel: "error",
		},
		TURN: TURNConfig{
			Enabled: false,
		},
		NodeSelector: NodeSelectorConfig{
			Kind:         "random",
			SysloadLimit: 0.9,
		},
		Keys: map[string]string{},
	}
	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, fmt.Errorf("could not parse config: %v", err)
		}
	}

	if c != nil {
		if err := conf.updateFromCLI(c); err != nil {
			return nil, err
		}
	}

	// expand env vars in filenames
	file, err := homedir.Expand(os.ExpandEnv(conf.KeyFile))
	if err != nil {
		return nil, err
	}
	conf.KeyFile = file

	// set defaults for ports if none are set
	if conf.RTC.UDPPort == 0 && conf.RTC.ICEPortRangeStart == 0 {
		// to make it easier to run in dev mode/docker, default to single port
		if conf.Development {
			conf.RTC.UDPPort = 7882
		} else {
			conf.RTC.ICEPortRangeStart = 50000
			conf.RTC.ICEPortRangeEnd = 60000
		}
	}

	if conf.RTC.NodeIP == "" {
		conf.RTC.NodeIP, err = conf.determineIP()
		if err != nil {
			return nil, err
		}
	}

	if conf.LogLevel != "" {
		conf.Logging.Level = conf.LogLevel
	}
	if conf.Logging.Level == "" && conf.Development {
		conf.Logging.Level = "debug"
	}

	return conf, nil
}

func (conf *Config) HasRedis() bool {
	return conf.Redis.Address != ""
}

func (conf *Config) updateFromCLI(c *cli.Context) error {
	if c.IsSet("dev") {
		conf.Development = c.Bool("dev")
	}
	if c.IsSet("key-file") {
		conf.KeyFile = c.String("key-file")
	}
	if c.IsSet("keys") {
		if err := conf.unmarshalKeys(c.String("keys")); err != nil {
			return errors.New("Could not parse keys, it needs to be exactly, \"key: secret\", including the space")
		}
	}
	if c.IsSet("region") {
		conf.Region = c.String("region")
	}
	if c.IsSet("redis-host") {
		conf.Redis.Address = c.String("redis-host")
	}
	if c.IsSet("redis-password") {
		conf.Redis.Password = c.String("redis-password")
	}
	if c.IsSet("turn-cert") {
		conf.TURN.CertFile = c.String("turn-cert")
	}
	if c.IsSet("turn-key") {
		conf.TURN.KeyFile = c.String("turn-key")
	}
	if c.IsSet("node-ip") {
		conf.RTC.NodeIP = c.String("node-ip")
	}
	if c.IsSet("udp-port") {
		conf.RTC.UDPPort = uint32(c.Int("udp-port"))
	}

	return nil
}

func (conf *Config) unmarshalKeys(keys string) error {
	temp := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(keys), temp); err != nil {
		return err
	}

	conf.Keys = make(map[string]string, len(temp))

	for key, val := range temp {
		if secret, ok := val.(string); ok {
			conf.Keys[key] = secret
		}
	}
	return nil
}
