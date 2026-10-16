package steam

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

type profileData struct {
	Player         player
	Friends        []friend
	FriendsPrivate bool
	FriendsError   error
	Games          []game
	Recent         []game
}

func (c *apiClient) player(ctx context.Context, sid string) (player, error) {
	var result playerResponse
	if err := c.get(ctx, "ISteamUser/GetPlayerSummaries/v2/", map[string]string{"steamids": sid}, &result); err != nil {
		return player{}, err
	}
	if result.Response == nil || len(result.Response.Players) != 1 || result.Response.Players[0].SteamID != sid {
		return player{}, errors.New("steam: player not found in summaries response")
	}
	p := result.Response.Players[0]
	if p.ProfileURL == "" {
		p.ProfileURL = "https://steamcommunity.com/profiles/" + sid + "/"
	}
	return p, nil
}

func (c *apiClient) games(ctx context.Context, path string, params map[string]string) ([]game, error) {
	var result gamesResponse
	if err := c.get(ctx, path, params, &result); err != nil {
		return nil, err
	}
	if result.Response == nil {
		return nil, errors.New("steam: missing games response")
	}
	// A successful empty response is an empty inventory, including after a privacy change.
	games := result.Response.Games
	sort.Slice(games, func(i, j int) bool { return games[i].AppID < games[j].AppID })
	for i, g := range games {
		if g.AppID == 0 || i > 0 && games[i-1].AppID == g.AppID {
			return nil, errors.New("steam: invalid or duplicate AppID in games")
		}
	}
	return games, nil
}

func (c *apiClient) friends(ctx context.Context, sid string) ([]friend, bool, error) {
	var result friendsResponse
	err := c.get(ctx, "ISteamUser/GetFriendList/v1/", map[string]string{"steamid": sid, "relationship": "all"}, &result)
	if httpx.IsStatus(err, http.StatusUnauthorized) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if result.FriendsList == nil {
		return nil, false, errors.New("steam: missing friends list response")
	}
	friends := result.FriendsList.Friends
	sort.Slice(friends, func(i, j int) bool { return friends[i].SteamID < friends[j].SteamID })
	return friends, false, nil
}

func (c *apiClient) profile(ctx context.Context, sid string) (profileData, error) {
	var data profileData
	var err error
	data.Player, err = c.player(ctx, sid)
	if err != nil {
		return data, err
	}
	data.Games, err = c.games(ctx, "IPlayerService/GetOwnedGames/v1/", map[string]string{"steamid": sid, "include_appinfo": "1", "include_played_free_games": "1"})
	if err != nil {
		return data, err
	}
	data.Recent, err = c.games(ctx, "IPlayerService/GetRecentlyPlayedGames/v1/", map[string]string{"steamid": sid, "count": "0"})
	if err != nil {
		return data, err
	}
	data.Friends, data.FriendsPrivate, data.FriendsError = c.friends(ctx, sid)
	return data, nil
}
