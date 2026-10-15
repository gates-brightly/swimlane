module github.com/gates-brightly/swimlane

go 1.27.1

require (
	filippo.io/age v1.3.2
	golang.org/x/crypto v0.57.0
	golang.org/x/net v0.60.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.46.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	filippo.io/hpke v0.4.0 // indirect
)

retract (
	v0.3.20261009 // tagged without bumping Breaking: it reports itself as 2.20261009
	v0.20261014.1 // doesn't build: internal/cli lost a fix in a rebase
)
