// Package kindle controls a jailbroken Kindle over SSH: status queries,
// display (fbink), rotation, and backlight.
package kindle

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Device is a Kindle reachable through an established SSH connection.
type Device struct {
	client                      CommandRunner
	mu                          sync.Mutex
	displayWidth, displayHeight int
	preparedImages              map[string][]byte
	preparedImageOrder          []string
	preparedImageBytes          int
}

type CommandRunner interface {
	Run(command string) (string, error)
}

type contextCommandRunner interface {
	RunContext(context.Context, string) (string, error)
}

type contextUploader interface {
	UploadContext(context.Context, string, []byte) error
}

func (d *Device) run(ctx context.Context, command string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if runner, ok := d.client.(contextCommandRunner); ok {
		return runner.RunContext(ctx, command)
	}
	return d.client.Run(command)
}

func (d *Device) upload(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if uploader, ok := d.client.(contextUploader); ok {
		return uploader.UploadContext(ctx, path, data)
	}
	if uploader, ok := d.client.(interface{ Upload(string, []byte) error }); ok {
		return uploader.Upload(path, data)
	}
	return fmt.Errorf("SSH client cannot upload files")
}

// New wraps an SSH client as a Kindle device.
func New(client CommandRunner) *Device {
	return &Device{
		client: client,
	}
}

// Uptime returns the device uptime string from the uptime command.
func (d *Device) Uptime() (string, error) {
	return d.UptimeContext(context.Background())
}

func (d *Device) UptimeContext(ctx context.Context) (string, error) {
	output, err := d.run(ctx, "uptime")
	if err != nil {
		return "", fmt.Errorf("get uptime: %w", err)
	}

	return strings.TrimSpace(output), nil
}

// Battery returns the charge level from gasgauge-info -c.
func (d *Device) Battery() (string, error) {
	return d.BatteryContext(context.Background())
}

func (d *Device) BatteryContext(ctx context.Context) (string, error) {
	output, err := d.run(ctx, "gasgauge-info -c")
	if err != nil {
		return "", fmt.Errorf("get battery: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// RotationState returns the current orientation as "vertical" or "horizontal"
// by reading /sys/class/graphics/fb0/rotate.
func (d *Device) RotationState() (string, error) {
	return d.RotationStateContext(context.Background())
}

func (d *Device) RotationStateContext(ctx context.Context) (string, error) {
	output, err := d.run(ctx, "cat /sys/class/graphics/fb0/rotate")
	if err != nil {
		return "", fmt.Errorf("get rotation state: %w", err)
	}
	name, ok := mapRotation(output)
	if !ok {
		return "", fmt.Errorf("get rotation state: unexpected value %q", strings.TrimSpace(output))
	}
	return name, nil
}

// SetRotation sets the framebuffer orientation.
// state must be "vertical" or "horizontal".
func (d *Device) SetRotation(state string) error {
	return d.SetRotationContext(context.Background(), state)
}

func (d *Device) SetRotationContext(ctx context.Context, state string) error {
	code, ok := rotationCode(state)
	if !ok {
		return fmt.Errorf("set rotation: invalid state %q (want %q or %q)", state, "vertical", "horizontal")
	}
	cmd := fmt.Sprintf("echo %s > /sys/class/graphics/fb0/rotate", code)
	if _, err := d.run(ctx, cmd); err != nil {
		return fmt.Errorf("set rotation: %w", err)
	}
	return nil
}

// ShowTitle draws a centered title on the e-ink screen via fbink.
func (d *Device) ShowTitle(title string) error {
	return d.ShowTitleContext(context.Background(), title)
}

func (d *Device) ShowTitleContext(ctx context.Context, title string) error {
	cmd := fmt.Sprintf(`/mnt/us/usbnet/bin/fbink -q -t regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=200,top=0,left=0,right=0,bottom=0 -m -- %s`, shellQuote(title))
	if _, err := d.run(ctx, cmd); err != nil {
		return fmt.Errorf("show title: %w", err)
	}
	return nil
}

// ShowDomains draws domain text below the title via fbink.
func (d *Device) ShowDomains(domains string) error {
	return d.ShowDomainsContext(context.Background(), domains)
}

func (d *Device) ShowDomainsContext(ctx context.Context, domains string) error {
	cmd := fmt.Sprintf(`/mnt/us/usbnet/bin/fbink -q -t regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=130,top=400,left=0,right=0,bottom=0 -m -- %s`, shellQuote(domains))
	if _, err := d.run(ctx, cmd); err != nil {
		return fmt.Errorf("show domains: %w", err)
	}
	return nil
}

// ShowDescription draws description text on the lower portion of the screen via fbink.
func (d *Device) ShowDescription(description string) error {
	return d.ShowDescriptionContext(context.Background(), description)
}

func (d *Device) ShowDescriptionContext(ctx context.Context, description string) error {
	cmd := fmt.Sprintf(`/mnt/us/usbnet/bin/fbink -q -t regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=150,top=500,left=0,right=0,bottom=0 -m -- %s`, shellQuote(description))
	if _, err := d.run(ctx, cmd); err != nil {
		return fmt.Errorf("show description: %w", err)
	}
	return nil
}

// ShowGenre draws genre text in a large mid-screen region via fbink.
func (d *Device) ShowGenre(genre string) error {
	return d.ShowGenreContext(context.Background(), genre)
}

func (d *Device) ShowGenreContext(ctx context.Context, genre string) error {
	cmd := fmt.Sprintf(`/mnt/us/usbnet/bin/fbink -q -t regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=280,top=250,left=0,right=0,bottom=250 -m -- %s`, shellQuote(genre))
	if _, err := d.run(ctx, cmd); err != nil {
		return fmt.Errorf("show genre: %w", err)
	}
	return nil
}

// ClearScreen fully clears and refreshes the e-ink display via fbink (GC16 waveform).
func (d *Device) ClearScreen() error {
	return d.ClearScreenContext(context.Background())
}

func (d *Device) ClearScreenContext(ctx context.Context) error {
	if _, err := d.run(ctx, `/mnt/us/usbnet/bin/fbink -q -c -f -W GC16`); err != nil {
		return fmt.Errorf("clear screen: %w", err)
	}
	return nil
}

// SetBacklight sets front-light intensity (0–24) via powerd lipc.
func (d *Device) SetBacklight(level int) error {
	return d.SetBacklightContext(context.Background(), level)
}

func (d *Device) SetBacklightContext(ctx context.Context, level int) error {
	if level < 0 || level > 24 {
		return fmt.Errorf("set backlight: level %d out of range 0..24", level)
	}
	cmd := fmt.Sprintf("lipc-set-prop com.lab126.powerd flIntensity %d", level)
	if _, err := d.run(ctx, cmd); err != nil {
		return fmt.Errorf("set backlight: %w", err)
	}
	return nil
}

// GetBrightness returns the current front-light intensity from powerd.
func (d *Device) GetBrightness() (int, error) {
	return d.GetBrightnessContext(context.Background())
}

func (d *Device) GetBrightnessContext(ctx context.Context) (int, error) {
	output, err := d.run(ctx, "lipc-get-prop com.lab126.powerd flIntensity")
	if err != nil {
		return 0, fmt.Errorf("get brightness: %w", err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		return 0, fmt.Errorf("get brightness: parse %q: %w", strings.TrimSpace(output), err)
	}
	return value, nil
}

// rotationCodes maps orientation names to fb0 rotate values.
var rotationCodes = map[string]string{
	"vertical":   "3",
	"horizontal": "0",
}

// mapRotation converts a sysfs rotate code to a name ("vertical" / "horizontal").
func mapRotation(value string) (string, bool) {
	for name, code := range rotationCodes {
		if strings.TrimSpace(value) == code {
			return name, true
		}
	}
	return "", false
}

// rotationCode converts an orientation name to its sysfs rotate code.
func rotationCode(name string) (string, bool) {
	code, ok := rotationCodes[strings.ToLower(strings.TrimSpace(name))]
	return code, ok
}
