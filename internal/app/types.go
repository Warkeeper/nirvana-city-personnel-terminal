package app

import "time"

const (
	KindPlayer = "player"
	KindNPC    = "npc"

	GoldIn       = "in"
	GoldOut      = "out"
	GoldForfeit  = "forfeit"
	GoldAllocate = "allocate"

	DefaultIdentity = "未设置"
)

var DefaultVisibleNPCCodes = []string{
	"999306",
	"999005",
	"999002",
	"999003",
	"999004",
	"999001",
	"999006",
	"999007",
	"999012",
	"999010",
	"999968",
	"999017",
	"999018",
	"999019",
	"999020",
	"999021",
	"999023",
	"999024",
	"999025",
}

type CitySession struct {
	ID       int64  `json:"id"`
	OpenedAt string `json:"openedAt"`
	ClosedAt string `json:"closedAt,omitempty"`
	Operator string `json:"operator"`
}

type LatestOperation struct {
	Time    string `json:"time"`
	Content string `json:"content"`
}

type TimeIncreaseLog struct {
	ID      int64  `json:"id,omitempty"`
	Time    string `json:"time"`
	Minutes int    `json:"minutes"`
}

type RoleDTO struct {
	ID              string            `json:"id"`
	TravelID        int64             `json:"travelId,omitempty"`
	Name            string            `json:"name"`
	Code            string            `json:"code"`
	Type            string            `json:"type"`
	Balance         int64             `json:"balance"`
	IdentityCurrent string            `json:"identityCurrent"`
	IdentityHistory []IdentityDTO     `json:"identityHistoryItems,omitempty"`
	IdentityTexts   []string          `json:"identityHistory"`
	Remark          string            `json:"remark"`
	StayHours       float64           `json:"stayHours,omitempty"`
	EnterTime       string            `json:"enterTime,omitempty"`
	LeaveTime       string            `json:"leaveTime,omitempty"`
	TimeIncreaseLog []TimeIncreaseLog `json:"timeIncreaseLogs,omitempty"`
}

type IdentityDTO struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Identity  string `json:"identity"`
	Occurred  string `json:"occurredAt"`
	Display   string `json:"display"`
	DeletedAt string `json:"deletedAt,omitempty"`
}

type GoldRecordDTO struct {
	ID            int64  `json:"id"`
	RoleID        string `json:"roleId"`
	Time          string `json:"time"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Identity      string `json:"identity"`
	Type          string `json:"type"`
	TypeCode      string `json:"typeCode"`
	Amount        int64  `json:"amount"`
	Balance       int64  `json:"balance"`
	Remark        string `json:"remark"`
	AffectBalance bool   `json:"affectBalance"`
	Voided        bool   `json:"voided"`
	Operator      string `json:"operator"`
}

type SessionDTO struct {
	Roles                         []RoleDTO        `json:"roles"`
	Records                       []GoldRecordDTO  `json:"records"`
	LatestOperation               *LatestOperation `json:"latestOperation,omitempty"`
	SessionStartedAt              string           `json:"sessionStartedAt"`
	Theme                         string           `json:"theme"`
	HiddenResidentCodes           []string         `json:"hiddenResidentCodes"`
	HiddenNPCKeys                 []string         `json:"hiddenNpcKeys"`
	SuppressedTravelResidentCodes []string         `json:"suppressedTravelResidentCodes"`
	HistoricalNPCs                []RoleDTO        `json:"historicalNpcs"`
	VisibleNPCCodes               []string         `json:"visibleNpcCodes"`
	CurrentSession                *CitySession     `json:"currentSession,omitempty"`
}

type TodayStats struct {
	TodayEntered  int   `json:"todayEntered"`
	CurrentInCity int   `json:"currentInCity"`
	DailyExpense  int64 `json:"dailyExpense"`
}

type AppState struct {
	CSRFToken          string     `json:"csrfToken,omitempty"`
	Session            SessionDTO `json:"session"`
	HistoricalPlayers  []RoleDTO  `json:"historicalPlayers"`
	Stats              TodayStats `json:"stats"`
	ServerTime         string     `json:"serverTime"`
	SchemaVersion      int        `json:"schemaVersion"`
	Operator           string     `json:"operator"`
	DefaultVisibleNPCs []string   `json:"defaultVisibleNpcCodes"`
}

type Sheet struct {
	Name    string              `json:"name"`
	Columns []string            `json:"columns"`
	Rows    []map[string]string `json:"rows"`
}

type ExportData struct {
	Sheets []Sheet `json:"sheets"`
}

type Config struct {
	DataDir string
	Now     func() time.Time
}
