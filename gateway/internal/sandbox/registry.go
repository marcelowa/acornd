package sandbox

// DriverFactory builds an engine's driver from the install folder and the state folder a caller
// named. It is the seam a driver in another package registers through: the youki driver imports
// this package for the types it implements, so this package cannot import it back.
type DriverFactory func(dataDir, state string) Driver

// driverFactories is every engine this package builds itself. The youki engine is added by its
// package's init; a binary that offers it imports that package.
var driverFactories = map[string]DriverFactory{
	"microsandbox": func(dataDir, state string) Driver {
		driver := NewMicrosandbox(dataDir)
		if state != "" {
			driver = driver.WithStateDir(state)
		}
		return driver
	},
	"bwrap": func(dataDir, state string) Driver {
		driver := NewBwrap(dataDir)
		if state != "" {
			driver = driver.WithStateDir(state)
		}
		return driver
	},
}

// RegisterDriver adds an engine to Engine's table. An engine package calls it from init, and the
// binary that offers the engine imports that package for the side effect.
func RegisterDriver(name string, make DriverFactory) { driverFactories[name] = make }
