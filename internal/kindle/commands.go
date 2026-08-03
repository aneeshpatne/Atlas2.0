package kindle

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aneeshpatne/atlas/internal/sshclient"
)

type Device struct {
	client *sshclient.SSHClient
}

func New(client *sshclient.SSHClient) *Device {
	return &Device{
		client: client,
	}
}

func (d *Device) Uptime() (string, error) {
	output, err := d.client.Run("uptime")
	if err != nil {
		return "", fmt.Errorf("get uptime: %w", err)
	}

	return strings.TrimSpace(output), nil
}

func (d *Device) Battery() (string, error) {
	output, err := d.client.Run("gasgauge-info -c")
	if err != nil {
		return "", fmt.Errorf("get battery: %w", err)
	}
	return strings.TrimSpace(output), nil
}

func (d *Device) RotationState() (string, error) {
	output, err := d.client.Run("cat /sys/class/graphics/fb0/rotate")
	if err != nil {
		return "", fmt.Errorf("get rotation state: %w", err)
	}
	name, ok := mapRotation(output)
	if !ok {
		return "", fmt.Errorf("get rotation state: unexpected value %q", strings.TrimSpace(output))
	}
	return name, nil
}

func (d *Device) SetRotation(state string) error {
	code, ok := rotationCode(state)
	if !ok {
		return fmt.Errorf("set rotation: invalid state %q (want %q or %q)", state, "vertical", "horizontal")
	}
	cmd := fmt.Sprintf("echo %s > /sys/class/graphics/fb0/rotate", code)
	if _, err := d.client.Run(cmd); err != nil {
		return fmt.Errorf("set rotation: %w", err)
	}
	return nil
}

func (d *Device) ShowTitle(title string) error {
	cmd := fmt.Sprintf(`/mnt/us/usbnet/bin/fbink -q -t regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=200,top=0,left=0,right=0,bottom=0 -m "%s"`, title)
	if _, err := d.client.Run(cmd); err != nil {
		return fmt.Errorf("show title: %w", err)
	}
	return nil
}

func (d *Device) ShowDomains(domains string) error {
	cmd := fmt.Sprintf(`/mnt/us/usbnet/bin/fbink -q -t regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=130,top=400,left=0,right=0,bottom=0 -m "%s"`, domains)
	if _, err := d.client.Run(cmd); err != nil {
		return fmt.Errorf("show domains: %w", err)
	}
	return nil
}

func (d *Device) ShowDescription(description string) error {
	cmd := fmt.Sprintf(`/mnt/us/usbnet/bin/fbink -q -t regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=150,top=500,left=0,right=0,bottom=0 -m "%s"`, description)
	if _, err := d.client.Run(cmd); err != nil {
		return fmt.Errorf("show description: %w", err)
	}
	return nil
}

func (d *Device) ShowGenre(genre string) error {
	cmd := fmt.Sprintf(`/mnt/us/usbnet/bin/fbink -q -t regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=280,top=250,left=0,right=0,bottom=250 -m "%s"`, genre)
	if _, err := d.client.Run(cmd); err != nil {
		return fmt.Errorf("show genre: %w", err)
	}
	return nil
}

func (d *Device) ClearScreen() error {
	if _, err := d.client.Run(`/mnt/us/usbnet/bin/fbink -q -c -f -W GC16`); err != nil {
		return fmt.Errorf("clear screen: %w", err)
	}
	return nil
}

func (d *Device) SetBacklight(level int) error {
	if level < 0 || level > 24 {
		return fmt.Errorf("set backlight: level %d out of range 0..24", level)
	}
	cmd := fmt.Sprintf("lipc-set-prop com.lab126.powerd flIntensity %d", level)
	if _, err := d.client.Run(cmd); err != nil {
		return fmt.Errorf("set backlight: %w", err)
	}
	return nil
}

func (d *Device) GetBrightness() (int, error) {
	output, err := d.client.Run("lipc-get-prop com.lab126.powerd flIntensity")
	if err != nil {
		return 0, fmt.Errorf("get brightness: %w", err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		return 0, fmt.Errorf("get brightness: parse %q: %w", strings.TrimSpace(output), err)
	}
	return value, nil
}

var rotationCodes = map[string]string{
	"vertical":   "3",
	"horizontal": "0",
}

func mapRotation(value string) (string, bool) {
	for name, code := range rotationCodes {
		if strings.TrimSpace(value) == code {
			return name, true
		}
	}
	return "", false
}

func rotationCode(name string) (string, bool) {
	code, ok := rotationCodes[strings.ToLower(strings.TrimSpace(name))]
	return code, ok
}
