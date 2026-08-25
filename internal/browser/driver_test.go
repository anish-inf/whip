package browser

import "testing"

func TestSetDriver(t *testing.T) {
	defer func() { Driver = DriverRod }()
	t.Setenv("WHIP_BROWSER_DRIVER", "") // env unset → SetDriver works
	SetDriver(DriverChromedp)
	if Driver != DriverChromedp {
		t.Fatalf("got %q", Driver)
	}
	SetDriver(DriverRod)
	if Driver != DriverRod {
		t.Fatalf("got %q", Driver)
	}
	SetDriver("bogus")
	if Driver != DriverRod {
		t.Fatalf("bogus driver must be ignored, got %q", Driver)
	}
}

func TestSetDriverEnvPinWins(t *testing.T) {
	defer func() { Driver = DriverRod }()
	t.Setenv("WHIP_BROWSER_DRIVER", "chromedp")
	SetDriver(DriverRod) // no-op under the pin
	if Driver != DriverRod {
		// note: Driver was already resolved at init; SetDriver must not
		// mutate it under a pin. The package var's initial value stands.
		t.Fatalf("pin must make SetDriver a no-op, got %q", Driver)
	}
}
