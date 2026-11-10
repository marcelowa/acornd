package sandbox

// DriverFactory builds an engine's driver from the install folder and the state folder a caller
// named. It is the seam a driver in another package registers through: a driver package imports
// this package for the types it implements, so this package cannot import it back.
type DriverFactory func(dataDir, state string) Driver

// driverFactories is every engine a binary has registered. Each driver package exports Register
// and the binary calls it before anything can choose an engine; nothing is registered here, so
// this package does not depend on any driver.
var driverFactories = map[string]DriverFactory{}

// RegisterDriver adds an engine to Engine's table. It is idempotent: a binary that registers the
// same engine twice still has one factory.
func RegisterDriver(name string, make DriverFactory) { driverFactories[name] = make }
