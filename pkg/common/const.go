package common

const (
	MaxLogFileSize = 100
	MaxLogFileAge  = 7
)

const EnvironmentDevelopment = "development"

const DefaultMessageLength = 50

type Mode string

const (
	ModeSingle      Mode = "single"
	ModeDistributed Mode = "distributed"
)

func (m Mode) String() string { return string(m) }

func (m Mode) IsDistributed() bool { return m == ModeDistributed }

type ProbeState string

const (
	ProbeStateOK       ProbeState = "ok"
	ProbeStateNotReady ProbeState = "not_ready"
)

func (s ProbeState) String() string { return string(s) }

type DumpFormat string

const (
	DumpFormatBinary DumpFormat = "binary"
	DumpFormatJSON   DumpFormat = "json"
)

func (f DumpFormat) String() string { return string(f) }
