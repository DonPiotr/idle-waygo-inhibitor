# CLAUDE.md

Tool CLI `idle-waygo-inhibitor` in Go che inibisce l'idle di Wayland durante le presentazioni.

## Opzioni

```
idle-waygo-inhibitor [-n] <start|stop|toggle|status>
```

- **-n** - invia una notifica desktop (`notify-send`) dopo i comandi che cambiano stato
- **start** - avvia il daemon in background (inibisce idle)
- **stop** - ferma il daemon inviando SIGTERM
- **toggle** - avvia se era fermo, ferma se era avviato
- **status** - restituisce lo stato (running / stopped)

## Architettura

Il daemon gira in background e mantiene aperta una richiesta di inibizione idle tramite il protocollo Wayland `wayland-idle-inhibit-unstable-v1`.

I sottocomandi `stop`/`toggle`/`status` trovano il daemon tramite **pgrep** (`pgrep -f "idle-waygo-inhibitor --daemon"`), escludendo il PID del processo corrente (`os.Getpid()`).

### Perché serve un layer surface

Il protocollo `zwp_idle_inhibitor_v1` funziona solo su superfici **mappate** (con ruolo e buffer committati). Una `wl_surface` nuda, senza ruolo, viene ignorata dal compositor.

Il daemon crea quindi:
1. Una **layer surface** (`zwlr_layer_shell_v1`, layer `BACKGROUND`, 1×1 px, nessun input) — fornisce il ruolo alla superficie.
2. Un **buffer condiviso** (`wl_shm`) di un pixel trasparente (ARGB8888 `0x00000000`) — mappa la superficie.
3. Lo **idle inhibitor** (`zwp_idle_inhibitor_v1`) agganciato alla superficie mappata.

Il layer shell non è incluso in go-wayland (wlr-protocols), quindi i bindings `LayerShell` / `LayerSurface` sono implementati a mano in `layershell.go` tramite `WriteMsg` raw.

### Bug niri: registryBind custom

go-wayland codifica la lunghezza della stringa nel `wl_registry.bind` come lunghezza _padded_, ma i compositor basati su smithay (niri) si aspettano lunghezza _actual_ (incluso il null terminator). La funzione `registryBind` in `main.go` corregge questo comportamento.

## Comunicazione tra CLI e daemon

- `start` → avvia il daemon in background (`--daemon`), con nuova sessione (`Setsid`)
- `stop` → trova il daemon via pgrep, invia SIGTERM
- `toggle` → se il daemon è vivo: SIGTERM; se assente: avvia
- `status` → trova il daemon via pgrep, stampa running / stopped

## Stack tecnico

- Linguaggio: **Go**
- Protocollo idle: `wayland-idle-inhibit-unstable-v1` (`zwp_idle_inhibitor_v1`)
- Protocollo surface: `wlr-layer-shell-unstable-v1` (`zwlr_layer_shell_v1`) — binding manuale in `layershell.go`
- Libreria Wayland: **go-wayland** (`github.com/rajveermalviya/go-wayland`)
- Notifiche: `notify-send` (opzionale, flag `-n`)
- Build: `go build`
- Target: Wayland only (sway, hyprland, niri, ecc.)
