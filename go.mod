module github.com/ellanetworks/smsc

go 1.26.5

require (
	github.com/ellanetworks/smsc/diameter v0.0.0-00010101000000-000000000000
	github.com/mattn/go-sqlite3 v1.14.42
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/ellanetworks/core/sctp v0.0.0-20260927202635-ae35a22e7995

replace github.com/ellanetworks/smsc/diameter => ./diameter
