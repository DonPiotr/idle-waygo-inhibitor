package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	idle_inhibit "github.com/rajveermalviya/go-wayland/wayland/unstable/idle-inhibit-v1"
)

func main() {
	args := os.Args[1:]

	notify := false
	if len(args) > 0 && args[0] == "-n" {
		notify = true
		args = args[1:]
	}

	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-n] <start|stop|toggle|status>\n", os.Args[0])
		os.Exit(1)
	}

	// notifyBody is set to "activated"/"deactivated" for commands that change state.
	// For toggle we determine it before executing (daemon running → will be deactivated).
	var notifyBody string
	var err error

	switch args[0] {
	case "--daemon":
		err = runDaemon()
	case "start":
		err = cmdStart()
		notifyBody = "activated"
	case "stop":
		err = cmdStop()
		notifyBody = "deactivated"
	case "toggle":
		if findDaemon() != 0 {
			notifyBody = "deactivated"
		} else {
			notifyBody = "activated"
		}
		err = cmdToggle()
	case "status":
		err = cmdStatus()
		if findDaemon() != 0 {
			notifyBody = "activated"
		} else {
			notifyBody = "deactivated"
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if notify && notifyBody != "" {
		sendNotify("Presentation mode:", notifyBody)
	}
}

func sendNotify(summary, body string) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return
	}
	exec.Command("notify-send", summary, body).Run() //nolint:errcheck
}

// findDaemon returns the PID of the running daemon, or 0 if not found.
func findDaemon() int {
	out, err := exec.Command("pgrep", "-f", "idle-waygo-inhibitor --daemon").Output()
	if err != nil {
		return 0
	}
	self := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid == self {
			continue
		}
		return pid
	}
	return 0
}

func cmdStart() error {
	if pid := findDaemon(); pid != 0 {
		fmt.Printf("already running (pid %d)\n", pid)
		return nil
	}
	cmd := exec.Command(os.Args[0], "--daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Printf("started (pid %d)\n", cmd.Process.Pid)
	return nil
}

func cmdStop() error {
	pid := findDaemon()
	if pid == 0 {
		fmt.Println("not running")
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	fmt.Printf("stopped (pid %d)\n", pid)
	return nil
}

func cmdToggle() error {
	if findDaemon() != 0 {
		return cmdStop()
	}
	return cmdStart()
}

func cmdStatus() error {
	pid := findDaemon()
	if pid == 0 {
		fmt.Println("stopped")
	} else {
		fmt.Printf("running (pid %d)\n", pid)
	}
	return nil
}

func runDaemon() error {
	display, err := client.Connect("")
	if err != nil {
		return fmt.Errorf("wayland connect: %w", err)
	}
	defer display.Context().Close()

	registry, err := display.GetRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	type globalEntry struct {
		name    uint32
		iface   string
		version uint32
	}
	var globals []globalEntry

	registry.SetGlobalHandler(func(e client.RegistryGlobalEvent) {
		globals = append(globals, globalEntry{e.Name, e.Interface, e.Version})
	})

	if err := roundtrip(display); err != nil {
		return fmt.Errorf("roundtrip: %w", err)
	}

	var compositor *client.Compositor
	var shm *client.Shm
	var inhibitManager *idle_inhibit.IdleInhibitManager
	var layerShell *LayerShell

	for _, g := range globals {
		switch g.iface {
		case "wl_compositor":
			compositor = client.NewCompositor(display.Context())
			if err := registryBind(registry, g.name, g.iface, g.version, compositor); err != nil {
				return fmt.Errorf("bind wl_compositor: %w", err)
			}
		case "wl_shm":
			shm = client.NewShm(display.Context())
			if err := registryBind(registry, g.name, g.iface, g.version, shm); err != nil {
				return fmt.Errorf("bind wl_shm: %w", err)
			}
		case "zwp_idle_inhibit_manager_v1":
			inhibitManager = idle_inhibit.NewIdleInhibitManager(display.Context())
			if err := registryBind(registry, g.name, g.iface, g.version, inhibitManager); err != nil {
				return fmt.Errorf("bind zwp_idle_inhibit_manager_v1: %w", err)
			}
		case "zwlr_layer_shell_v1":
			layerShell = NewLayerShell(display.Context())
			if err := registryBind(registry, g.name, g.iface, g.version, layerShell); err != nil {
				return fmt.Errorf("bind zwlr_layer_shell_v1: %w", err)
			}
		}
	}

	if compositor == nil {
		return fmt.Errorf("wl_compositor not available")
	}
	if shm == nil {
		return fmt.Errorf("wl_shm not available")
	}
	if inhibitManager == nil {
		return fmt.Errorf("zwp_idle_inhibit_manager_v1 not available")
	}
	if layerShell == nil {
		return fmt.Errorf("zwlr_layer_shell_v1 not available")
	}

	// Create a 1×1 transparent pixel in shared memory.
	// The compositor maps this fd, so we keep the file open for the daemon's lifetime.
	shmFile, err := os.CreateTemp("", "idle-inhibitor-shm-*")
	if err != nil {
		return fmt.Errorf("create shm file: %w", err)
	}
	defer shmFile.Close()
	if _, err := shmFile.Write(make([]byte, 4)); err != nil {
		return fmt.Errorf("write shm file: %w", err)
	}
	pool, err := shm.CreatePool(int(shmFile.Fd()), 4)
	if err != nil {
		return fmt.Errorf("create shm pool: %w", err)
	}
	// stride=4 (4 bytes per row), format=ARGB8888 (0x00000000 = transparent black, premultiplied)
	pixelBuf, err := pool.CreateBuffer(0, 1, 1, 4, uint32(client.ShmFormatArgb8888))
	if err != nil {
		return fmt.Errorf("create shm buffer: %w", err)
	}

	// Create a wl_surface and give it a layer-shell role (BACKGROUND, 1×1, no input).
	// The compositor only honors zwp_idle_inhibitor_v1 on surfaces that are mapped
	// (have a role + committed buffer). A bare wl_surface without a role is ignored.
	surface, err := compositor.CreateSurface()
	if err != nil {
		return fmt.Errorf("create surface: %w", err)
	}
	defer surface.Destroy()

	layerSurf, err := layerShell.GetLayerSurface(surface, layerBackground, "idle-inhibitor")
	if err != nil {
		return fmt.Errorf("get layer surface: %w", err)
	}
	if err := layerSurf.SetSize(1, 1); err != nil {
		return fmt.Errorf("set layer size: %w", err)
	}
	if err := layerSurf.SetKeyboardInteractivity(keyboardInteractivityNone); err != nil {
		return fmt.Errorf("set keyboard interactivity: %w", err)
	}

	// When the compositor sends configure, ack it and commit the pixel buffer.
	// This maps the surface and makes the idle inhibitor effective.
	var configureErr error
	configured := false
	layerSurf.SetConfigureHandler(func(serial, _, _ uint32) {
		configured = true
		if err := layerSurf.AckConfigure(serial); err != nil {
			configureErr = err
			return
		}
		if err := surface.Attach(pixelBuf, 0, 0); err != nil {
			configureErr = err
			return
		}
		if err := surface.DamageBuffer(0, 0, 1, 1); err != nil {
			configureErr = err
			return
		}
		configureErr = surface.Commit()
	})

	// Initial commit — triggers the compositor to send configure.
	if err := surface.Commit(); err != nil {
		return fmt.Errorf("surface commit: %w", err)
	}
	// Roundtrip 1: compositor sends configure → handler acks + commits with buffer.
	if err := roundtrip(display); err != nil {
		return fmt.Errorf("roundtrip (configure): %w", err)
	}
	if configureErr != nil {
		return fmt.Errorf("configure handler: %w", configureErr)
	}
	if !configured {
		return fmt.Errorf("layer surface was not configured by compositor")
	}
	// Roundtrip 2: ensures the buffer commit is processed → surface is now mapped.
	if err := roundtrip(display); err != nil {
		return fmt.Errorf("roundtrip (map): %w", err)
	}

	// Surface is mapped. Attach the idle inhibitor.
	inhibitor, err := inhibitManager.CreateInhibitor(surface)
	if err != nil {
		return fmt.Errorf("create inhibitor: %w", err)
	}
	defer inhibitor.Destroy()

	// Roundtrip 3: flush inhibitor creation to the compositor.
	if err := roundtrip(display); err != nil {
		return fmt.Errorf("roundtrip (inhibitor): %w", err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)

	dispatchErr := make(chan error, 1)
	go func() {
		for {
			if err := display.Context().Dispatch(); err != nil {
				dispatchErr <- err
				return
			}
		}
	}()

	select {
	case <-sigs:
	case err := <-dispatchErr:
		return err
	}
	return nil
}

// registryBind sends a wl_registry.bind request with correct Wayland string encoding.
// go-wayland's Registry.Bind uses padded length in the string length field, which
// breaks smithay-based compositors (niri) that use CStr::from_bytes_with_nul.
func registryBind(registry *client.Registry, name uint32, iface string, version uint32, id client.Proxy) error {
	// Correct encoding: length field = len(iface)+1 (including null terminator).
	// Data = iface bytes + null, padded to 4-byte boundary.
	actualLen := len(iface) + 1
	paddedLen := (actualLen + 3) &^ 3
	msgLen := 8 + 4 + 4 + paddedLen + 4 + 4
	buf := make([]byte, msgLen)
	l := 0
	client.PutUint32(buf[l:], registry.ID())
	l += 4
	client.PutUint32(buf[l:], uint32(msgLen<<16))
	l += 4
	client.PutUint32(buf[l:], name)
	l += 4
	client.PutUint32(buf[l:], uint32(actualLen))
	l += 4
	copy(buf[l:], iface)
	l += paddedLen
	client.PutUint32(buf[l:], version)
	l += 4
	client.PutUint32(buf[l:], id.ID())
	return registry.Context().WriteMsg(buf, nil)
}

// roundtrip performs a synchronous Wayland roundtrip, processing all pending events.
func roundtrip(display *client.Display) error {
	done := false
	cb, err := display.Sync()
	if err != nil {
		return err
	}
	cb.SetDoneHandler(func(_ client.CallbackDoneEvent) {
		done = true
	})
	for !done {
		if err := display.Context().Dispatch(); err != nil {
			return err
		}
	}
	return nil
}
