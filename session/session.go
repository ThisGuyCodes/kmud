package session

import (
	"fmt"
	"io"
	"kmud/database"
	"kmud/model"
	"kmud/utils"
	"labix.org/v2/mgo/bson"
	"strconv"
	"strings"
	"time"
)

type Session struct {
	conn   io.ReadWriter
	user   *database.User
	player *database.Character
	room   *database.Room
	zone   *database.Zone
}

func NewSession(conn io.ReadWriter, user *database.User, player *database.Character) Session {
	var session Session
	session.conn = conn
	session.user = user
	session.player = player
	session.room = model.M.GetRoom(player.GetRoomId())
	session.zone = model.M.GetZone(session.room.GetZoneId())

	return session
}

type userInputMode int

const (
	CleanUserInput userInputMode = iota
	RawUserInput   userInputMode = iota
)

func toggleExitMenu(cm utils.ColorMode, room *database.Room) utils.Menu {
	onOrOff := func(direction database.ExitDirection) string {
		text := "Off"
		if room.HasExit(direction) {
			text = "On"
		}
		return utils.Colorize(cm, utils.ColorBlue, text)
	}

	menu := utils.NewMenu("Edit Exits")

	menu.AddAction("n", "[N]orth: "+onOrOff(database.DirectionNorth))
	menu.AddAction("ne", "[NE]North East: "+onOrOff(database.DirectionNorthEast))
	menu.AddAction("e", "[E]ast: "+onOrOff(database.DirectionEast))
	menu.AddAction("se", "[SE]South East: "+onOrOff(database.DirectionSouthEast))
	menu.AddAction("s", "[S]outh: "+onOrOff(database.DirectionSouth))
	menu.AddAction("sw", "[SW]South West: "+onOrOff(database.DirectionSouthWest))
	menu.AddAction("w", "[W]est: "+onOrOff(database.DirectionWest))
	menu.AddAction("nw", "[NW]North West: "+onOrOff(database.DirectionNorthWest))
	menu.AddAction("u", "[U]p: "+onOrOff(database.DirectionUp))
	menu.AddAction("d", "[D]own: "+onOrOff(database.DirectionDown))

	return menu
}

func npcMenu(room *database.Room) utils.Menu {
	npcs := model.M.NpcsIn(room)

	menu := utils.NewMenu("NPCs")

	menu.AddAction("n", "[N]ew")

	for i, npc := range npcs {
		index := i + 1
		actionText := fmt.Sprintf("[%v]%v", index, npc.PrettyName())
		menu.AddActionData(index, actionText, npc.GetId())
	}

	return menu
}

func specificNpcMenu(npcId bson.ObjectId) utils.Menu {
	npc := model.M.GetCharacter(npcId)
	menu := utils.NewMenu(npc.PrettyName())
	menu.AddAction("r", "[R]ename")
	menu.AddAction("d", "[D]elete")
	menu.AddAction("c", "[C]onversation")
	return menu
}

func (session *Session) Exec() {
	processEvent := func(event model.Event) string {
		message := event.ToString(session.player)

		switch event.Type() {
		case model.RoomUpdateEventType:
			roomEvent := event.(model.RoomUpdateEvent)
			if roomEvent.Room.GetId() == session.room.GetId() {
				session.room = roomEvent.Room
			}
		}

		return message
	}

	eventChannel := model.Register(session.player)
	defer model.Unregister(eventChannel)

	userInputChannel := make(chan string)
	promptChannel := make(chan string)

	inputModeChannel := make(chan userInputMode)
	panicChannel := make(chan interface{})

	/**
	 * Allows us to retrieve user input in a way that doesn't block the
	 * event loop by using channels and a separate Go routine to grab
	 * either the next user input or the next event.
	 */
	getUserInput := func(inputMode userInputMode, prompt string) string {
		inputModeChannel <- inputMode
		promptChannel <- prompt

		for {
			select {
			case input := <-userInputChannel:
				return input
			case event := <-eventChannel:
				message := processEvent(event)
				if message != "" {
					session.clearLine()
					session.printLine(message)
					session.printString(prompt)
				}
			case quitMessage := <-panicChannel:
				panic(quitMessage)
			}
		}
		panic("Unexpected code path")
	}

	// Same behavior as menu.Exec(), except that it uses getUserInput
	// which doesn't block the event loop while waiting for input
	execMenu := func(menu utils.Menu) (string, bson.ObjectId) {
		choice := ""
		var data bson.ObjectId

		for {
			menu.Print(session.conn, session.user.GetColorMode())
			choice = getUserInput(CleanUserInput, menu.GetPrompt())
			if menu.HasAction(choice) || choice == "" {
				data = menu.GetData(choice)
				break
			}
		}
		return choice, data
	}

	processCommand := func(command string, args []string) {
		switch command {
		case "help":
		case "edit":
			session.printRoomEditor()

			for {
				input := getUserInput(CleanUserInput, "Select a section to edit: ")

				switch input {
				case "":
					session.printRoom()
					return

				case "1":
					input = getUserInput(RawUserInput, "Enter new title: ")

					if input != "" {
						session.room.SetTitle(input)
					}
					session.printRoomEditor()

				case "2":
					input = getUserInput(RawUserInput, "Enter new description: ")

					if input != "" {
						session.room.SetDescription(input)
					}
					session.printRoomEditor()

				case "3":
					for {
						menu := toggleExitMenu(session.user.GetColorMode(), session.room)

						choice, _ := execMenu(menu)

						if choice == "" {
							break
						}

						direction := database.StringToDirection(choice)
						if direction != database.DirectionNone {
							enable := !session.room.HasExit(direction)
							session.room.SetExitEnabled(direction, enable)

							// Disable the corresponding exit in the adjacent room if necessary
							loc := session.room.NextLocation(direction)
							otherRoom := model.M.GetRoomByLocation(loc, session.zone)
							if otherRoom != nil {
								otherRoom.SetExitEnabled(direction.Opposite(), enable)
							}
						}
					}

					session.printRoomEditor()

				default:
					session.printLine("Invalid selection")
				}
			}

			// Quick room/exit creation
		case "/n":
			session.room.SetExitEnabled(database.DirectionNorth, true)
			session.handleAction("n", []string{})
			session.room.SetExitEnabled(database.DirectionSouth, true)

		case "/e":
			session.room.SetExitEnabled(database.DirectionEast, true)
			session.handleAction("e", []string{})
			session.room.SetExitEnabled(database.DirectionWest, true)

		case "/s":
			session.room.SetExitEnabled(database.DirectionSouth, true)
			session.handleAction("s", []string{})
			session.room.SetExitEnabled(database.DirectionNorth, true)

		case "/w":
			session.room.SetExitEnabled(database.DirectionWest, true)
			session.handleAction("w", []string{})
			session.room.SetExitEnabled(database.DirectionEast, true)

		case "/u":
			session.room.SetExitEnabled(database.DirectionUp, true)
			session.handleAction("u", []string{})
			session.room.SetExitEnabled(database.DirectionDown, true)

		case "/d":
			session.room.SetExitEnabled(database.DirectionDown, true)
			session.handleAction("d", []string{})
			session.room.SetExitEnabled(database.DirectionUp, true)

		case "/ne":
			session.room.SetExitEnabled(database.DirectionNorthEast, true)
			session.handleAction("ne", []string{})
			session.room.SetExitEnabled(database.DirectionSouthWest, true)

		case "/nw":
			session.room.SetExitEnabled(database.DirectionNorthWest, true)
			session.handleAction("nw", []string{})
			session.room.SetExitEnabled(database.DirectionSouthEast, true)

		case "/se":
			session.room.SetExitEnabled(database.DirectionSouthEast, true)
			session.handleAction("se", []string{})
			session.room.SetExitEnabled(database.DirectionNorthWest, true)

		case "/sw":
			session.room.SetExitEnabled(database.DirectionSouthWest, true)
			session.handleAction("sw", []string{})
			session.room.SetExitEnabled(database.DirectionNorthEast, true)

		case "loc":
			fallthrough
		case "location":
			session.printLine("%v", session.room.GetLocation())

		case "map":
			mapUsage := func() {
				session.printError("Usage: /map [<radius>|all|load <name>]")
			}

			startX := 0
			startY := 0
			startZ := 0
			endX := 0
			endY := 0
			endZ := 0

			if len(args) == 0 {
				args = append(args, "10")
			}

			if len(args) == 1 {
				radius, err := strconv.Atoi(args[0])

				if err == nil && radius > 0 {
					startX = session.room.GetLocation().X - radius
					startY = session.room.GetLocation().Y - radius
					startZ = session.room.GetLocation().Z
					endX = startX + (radius * 2)
					endY = startY + (radius * 2)
					endZ = session.room.GetLocation().Z
				} else if args[0] == "all" {
					topLeft, bottomRight := model.ZoneCorners(session.zone.GetId())

					startX = topLeft.X
					startY = topLeft.Y
					startZ = topLeft.Z
					endX = bottomRight.X
					endY = bottomRight.Y
					endZ = bottomRight.Z
				} else {
					mapUsage()
					return
				}
			} else {
				mapUsage()
				return
			}

			width := endX - startX + 1
			height := endY - startY + 1
			depth := endZ - startZ + 1

			builder := newMapBuilder(width, height, depth)
			builder.setUserRoom(session.room)

			for z := startZ; z <= endZ; z += 1 {
				for y := startY; y <= endY; y += 1 {
					for x := startX; x <= endX; x += 1 {
						loc := database.Coordinate{x, y, z}
						room := model.M.GetRoomByLocation(loc, session.zone)

						if room != nil {
							// Translate to 0-based coordinates and double the coordinate
							// space to leave room for the exit lines
							builder.addRoom(room, (x-startX)*2, (y-startY)*2, z-startZ)
						}
					}
				}
			}

			session.printLine(utils.TrimEmptyRows(builder.toString(session.user.GetColorMode())))

		case "zone":
			if len(args) == 0 {
				if session.zone.GetId() == "" {
					session.printLine("Currently in the null zone")
				} else {
					session.printLine("Current zone: " + utils.Colorize(session.user.GetColorMode(), utils.ColorBlue, session.zone.GetName()))
				}
			} else if len(args) == 1 {
				if args[0] == "list" {
					session.printLineColor(utils.ColorBlue, "Zones")
					session.printLineColor(utils.ColorBlue, "-----")
					for _, zone := range model.M.GetZones() {
						session.printLine(zone.GetName())
					}
				} else {
					session.printError("Usage: /zone [list|rename <name>|new <name>]")
				}
			} else if len(args) == 2 {
				if args[0] == "rename" {
					zone := model.M.GetZoneByName(args[0])

					if zone != nil {
						session.printError("A zone with that name already exists")
						return
					}

					if session.zone.GetId() == "" {
						session.zone = model.M.CreateZone(args[1])
						model.MoveRoomsToZone("", session.zone.GetId())
					} else {
						session.zone.SetName(args[1])
					}
				} else if args[0] == "new" {
					zone := model.M.GetZoneByName(args[0])

					if zone != nil {
						session.printError("A zone with that name already exists")
						return
					}

					newZone := model.M.CreateZone(args[1])
					newRoom := model.M.CreateRoom(newZone)

					model.MoveCharacterToRoom(session.player, newRoom)

					session.zone = newZone
					session.room = newRoom

					session.printRoom()
				}
			}

		case "broadcast":
			fallthrough
		case "b":
			if len(args) == 0 {
				session.printError("Nothing to say")
			} else {
				model.BroadcastMessage(session.player, strings.Join(args, " "))
			}

		case "say":
			fallthrough
		case "s":
			if len(args) == 0 {
				session.printError("Nothing to say")
			} else {
				model.Say(session.player, strings.Join(args, " "))
			}

		case "me":
			model.Emote(session.player, strings.Join(args, " "))

		case "whisper":
			fallthrough
		case "tell":
			fallthrough
		case "w":
			if len(args) < 2 {
				session.printError("Usage: /whisper <player> <message>")
				return
			}

			name := string(args[0])
			targetChar := model.M.GetCharacterByName(name)

			if targetChar == nil {
				session.printError("Player '%s' not found", name)
				return
			}

			if !targetChar.IsOnline() {
				session.printError("Player '%s' is not online", targetChar.PrettyName())
				return
			}

			message := strings.Join(args[1:], " ")
			model.Tell(session.player, targetChar, message)

		case "teleport":
			fallthrough
		case "tel":
			telUsage := func() {
				session.printError("Usage: /teleport [<zone>|<X> <Y> <Z>]")
			}

			x := 0
			y := 0
			z := 0

			newZone := session.zone

			if len(args) == 1 {
				newZone = model.M.GetZoneByName(args[0])

				if newZone == nil {
					session.printError("Zone not found")
					return
				}

				if newZone.GetId() == session.room.GetZoneId() {
					session.printLine("You're already in that zone")
					return
				}

				zoneRooms := model.M.GetRoomsInZone(newZone)

				if len(zoneRooms) > 0 {
					r := zoneRooms[0]
					x = r.GetLocation().X
					y = r.GetLocation().Y
					z = r.GetLocation().Z
				}
			} else if len(args) == 3 {
				var err error
				x, err = strconv.Atoi(args[0])

				if err != nil {
					telUsage()
					return
				}

				y, err = strconv.Atoi(args[1])

				if err != nil {
					telUsage()
					return
				}

				z, err = strconv.Atoi(args[2])

				if err != nil {
					telUsage()
					return
				}
			} else {
				telUsage()
				return
			}

			newRoom, err := model.MoveCharacterToLocation(session.player, newZone, database.Coordinate{X: x, Y: y, Z: z})

			if err == nil {
				session.room = newRoom
				session.zone = newZone
				session.printRoom()
			} else {
				session.printError(err.Error())
			}

		case "who":
			chars := model.M.GetOnlineCharacters()

			session.printLine("")
			session.printLine("Online Players")
			session.printLine("--------------")

			for _, char := range chars {
				session.printLine(char.PrettyName())
			}
			session.printLine("")

		case "colors":
			session.printLineColor(utils.ColorRed, "Red")
			session.printLineColor(utils.ColorDarkRed, "Dark Red")
			session.printLineColor(utils.ColorGreen, "Green")
			session.printLineColor(utils.ColorDarkGreen, "Dark Green")
			session.printLineColor(utils.ColorBlue, "Blue")
			session.printLineColor(utils.ColorDarkBlue, "Dark Blue")
			session.printLineColor(utils.ColorYellow, "Yellow")
			session.printLineColor(utils.ColorDarkYellow, "Dark Yellow")
			session.printLineColor(utils.ColorMagenta, "Magenta")
			session.printLineColor(utils.ColorDarkMagenta, "Dark Magenta")
			session.printLineColor(utils.ColorCyan, "Cyan")
			session.printLineColor(utils.ColorDarkCyan, "Dark Cyan")
			session.printLineColor(utils.ColorBlack, "Black")
			session.printLineColor(utils.ColorWhite, "White")
			session.printLineColor(utils.ColorGray, "Gray")

		case "colormode":
			fallthrough
		case "cm":
			if len(args) == 0 {
				message := "Current color mode is: "
				switch session.user.GetColorMode() {
				case utils.ColorModeNone:
					message = message + "None"
				case utils.ColorModeLight:
					message = message + "Light"
				case utils.ColorModeDark:
					message = message + "Dark"
				}
				session.printLine(message)
			} else if len(args) == 1 {
				switch strings.ToLower(args[0]) {
				case "none":
					session.user.SetColorMode(utils.ColorModeNone)
					session.printLine("Color mode set to: None")
				case "light":
					session.user.SetColorMode(utils.ColorModeLight)
					session.printLine("Color mode set to: Light")
				case "dark":
					session.user.SetColorMode(utils.ColorModeDark)
					session.printLine("Color mode set to: Dark")
				default:
					session.printLine("Valid color modes are: None, Light, Dark")
				}
			} else {
				session.printLine("Valid color modes are: None, Light, Dark")
			}

		case "destroyroom":
			if len(args) == 1 {
				direction := database.StringToDirection(args[0])

				if direction == database.DirectionNone {
					session.printError("Not a valid direction")
				} else {
					loc := session.room.NextLocation(direction)
					roomToDelete := model.M.GetRoomByLocation(loc, session.zone)
					if roomToDelete != nil {
						model.DeleteRoom(roomToDelete)
						session.printLine("Room destroyed")
					} else {
						session.printError("No room in that direction")
					}
				}
			} else {
				session.printError("Usage: /destroyroom <direction>")
			}

		case "npc":
			menu := npcMenu(session.room)
			choice, npcId := execMenu(menu)

			getName := func() string {
				name := ""
				for {
					name = getUserInput(CleanUserInput, "Desired NPC name: ")
					char := model.M.GetCharacterByName(name)

					if name == "" {
						return ""
					} else if char != nil {
						session.printError("That name is unavailable")
					} else if err := utils.ValidateName(name); err != nil {
						session.printError(err.Error())
					} else {
						break
					}
				}
				return name
			}

			if choice == "" {
				goto done
			}

			if choice == "n" {
				name := getName()
				if name == "" {
					goto done
				}
				model.M.CreateNpc(name, session.room)
			} else if npcId != "" {
				specificMenu := specificNpcMenu(npcId)
				choice, _ := execMenu(specificMenu)

				switch choice {
				case "d":
					model.M.DeleteCharacter(npcId)
				case "r":
					name := getName()
					if name == "" {
						break
					}
					npc := model.M.GetCharacter(npcId)
					npc.SetName(name)
				case "c":
					npc := model.M.GetCharacter(npcId)
					conversation := npc.GetConversation()

					if conversation == "" {
						conversation = "<empty>"
					}

					session.printLine("Conversation: %s", conversation)
					newConversation := getUserInput(RawUserInput, "New conversation text: ")

					if newConversation != "" {
						npc.SetConversation(newConversation)
					}
				}
			}

		done:
			session.printRoom()

		case "create":
			createUsage := func() {
				session.printError("Usage: /create <item name>")
			}

			if len(args) != 1 {
				createUsage()
				return
			}

			item := model.M.CreateItem(args[0])
			session.room.AddItem(item)
			session.printLine("Item created")

		case "destroyitem":
			destroyUsage := func() {
				session.printError("Usage: /destroyitem <item name>")
			}

			if len(args) != 1 {
				destroyUsage()
				return
			}

			itemsInRoom := model.M.GetItems(session.room.GetItemIds())
			name := strings.ToLower(args[0])

			for _, item := range itemsInRoom {
				if strings.ToLower(item.PrettyName()) == name {
					session.room.RemoveItem(item)
					model.M.DeleteItem(item.GetId())
					session.printLine("Item destroyed")
					return
				}
			}

			session.printError("Item not found")

		case "cash":
			cashUsage := func() {
				session.printError("Usage: /cash give <amount>")
			}

			if len(args) != 2 {
				cashUsage()
				return
			}

			if args[0] == "give" {
				amount, err := strconv.Atoi(args[1])

				if err != nil {
					cashUsage()
					return
				}

				session.player.AddCash(amount)
				session.printLine("Received: %v monies", amount)
			} else {
				cashUsage()
				return
			}

		case "roomid":
			session.printLine("Room ID: %v", session.room.GetId())

		default:
			session.printError("Unrecognized command: %s", command)
		}
	}

	session.printLineColor(utils.ColorWhite, "Welcome, "+session.player.PrettyName())
	session.printRoom()

	// Main routine in charge of actually reading input from the connection object,
	// also has built in throttling to limit how fast we are allowed to process
	// commands from the user. 
	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicChannel <- r
			}
		}()

		lastTime := time.Now()

		delay := time.Duration(200) * time.Millisecond

		for {
			mode := <-inputModeChannel
			prompt := utils.Colorize(session.user.GetColorMode(), utils.ColorWhite, <-promptChannel)
			input := ""

			switch mode {
			case CleanUserInput:
				input = utils.GetUserInput(session.conn, prompt)
			case RawUserInput:
				input = utils.GetRawUserInput(session.conn, prompt)
			default:
				panic("Unhandled case in switch statement (userInputMode)")
			}

			diff := time.Since(lastTime)

			if diff < delay {
				time.Sleep(delay - diff)
			}

			userInputChannel <- input
			lastTime = time.Now()
		}
	}()

	// Main loop
	for {
		input := getUserInput(RawUserInput, prompt())
		if input == "" || input == "logout" {
			return
		}
		if strings.HasPrefix(input, "/") {
			processCommand(utils.Argify(input[1:]))
		} else {
			session.handleAction(utils.Argify(input))
		}
	}
}

func (session *Session) printString(data string) {
	io.WriteString(session.conn, data)
}

func (session *Session) printLineColor(color utils.Color, line string, a ...interface{}) {
	utils.WriteLine(session.conn, utils.Colorize(session.user.GetColorMode(), color, fmt.Sprintf(line, a...)))
}

func (session *Session) printLine(line string, a ...interface{}) {
	session.printLineColor(utils.ColorWhite, line, a...)
}

func (session *Session) printError(err string, a ...interface{}) {
	session.printLineColor(utils.ColorRed, err, a...)
}

func (session *Session) printRoom() {
	playerList := model.M.PlayersIn(session.room, session.player)
	npcList := model.M.NpcsIn(session.room)
	session.printLine(session.room.ToString(database.ReadMode, session.user.GetColorMode(),
		playerList, npcList, model.M.GetItems(session.room.GetItemIds())))
}

func (session *Session) printRoomEditor() {
	session.printLine(session.room.ToString(database.EditMode, session.user.GetColorMode(), nil, nil, nil))
}

func (session *Session) clearLine() {
	utils.ClearLine(session.conn)
}

func prompt() string {
	return "> "
}

// vim: nocindent
