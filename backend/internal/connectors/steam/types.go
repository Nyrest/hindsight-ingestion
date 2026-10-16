package steam

type player struct {
	SteamID     string `json:"steamid"`
	Username    string `json:"personaname"`
	ProfileURL  string `json:"profileurl"`
	Avatar      string `json:"avatarfull"`
	Visibility  int    `json:"communityvisibilitystate"`
	RealName    string `json:"realname,omitempty"`
	Country     string `json:"loccountrycode,omitempty"`
	Created     int64  `json:"timecreated,omitempty"`
	LastLogoff  int64  `json:"lastlogoff,omitempty"`
	State       int    `json:"personastate"`
	GameID      string `json:"gameid,omitempty"`
	CurrentGame string `json:"gameextrainfo,omitempty"`
}
type playerResponse struct {
	Response *struct {
		Players []player `json:"players"`
	} `json:"response"`
}
type friend struct {
	SteamID      string `json:"steamid"`
	Relationship string `json:"relationship"`
	Since        int64  `json:"friend_since"`
}
type friendsResponse struct {
	FriendsList *struct {
		Friends []friend `json:"friends"`
	} `json:"friendslist"`
}
type game struct {
	AppID        uint32 `json:"appid"`
	Name         string `json:"name"`
	Icon         string `json:"img_icon_url,omitempty"`
	Playtime     int64  `json:"playtime_forever"`
	Recent       *int64 `json:"playtime_2weeks,omitempty"`
	Windows      *int64 `json:"playtime_windows_forever,omitempty"`
	Mac          *int64 `json:"playtime_mac_forever,omitempty"`
	Linux        *int64 `json:"playtime_linux_forever,omitempty"`
	Deck         *int64 `json:"playtime_deck_forever,omitempty"`
	Disconnected *int64 `json:"playtime_disconnected,omitempty"`
	LastPlayed   int64  `json:"rtime_last_played,omitempty"`
}
type gamesResponse struct {
	Response *struct {
		Games []game `json:"games"`
	} `json:"response"`
}
type achievementDefinition struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	Hidden      int    `json:"hidden"`
	Icon        string `json:"icon,omitempty"`
	LockedIcon  string `json:"icongray,omitempty"`
}
type gameSchema struct {
	UnavailableReason string `json:"unavailableReason,omitempty"`
	GameName          string `json:"gameName,omitempty"`
	Available         *struct {
		Achievements []achievementDefinition `json:"achievements"`
	} `json:"availableGameStats,omitempty"`
}
type schemaResponse struct {
	Game *gameSchema `json:"game"`
}
type playerAchievement struct {
	Name        string `json:"apiname"`
	Achieved    *int   `json:"achieved"`
	UnlockedAt  int64  `json:"unlocktime"`
	DisplayName string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}
type achievementResponse struct {
	PlayerStats *struct {
		SteamID      string              `json:"steamID"`
		Success      bool                `json:"success"`
		Error        string              `json:"error"`
		Achievements []playerAchievement `json:"achievements"`
	} `json:"playerstats"`
}
type achievement struct {
	APIName     string
	Name        string
	Description string
	Hidden      bool
	Icon        string
	LockedIcon  string
	State       string
	UnlockedAt  int64
}
type achievements struct {
	Status string
	Reason string
	Items  []achievement
}
type snapshot struct {
	PlaytimeHash string
	Achievements achievements
}
type cursor struct {
	Version       int      `json:"version"`
	SteamID       string   `json:"steamId"`
	Language      string   `json:"language"`
	CredentialKey string   `json:"credentialKey"`
	Games         []uint32 `json:"games"`
}
