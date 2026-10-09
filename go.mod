module github.com/gates-brightly/swimlane

go 1.27.1

require (
	golang.org/x/net v0.60.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.46.0
	gopkg.in/yaml.v3 v3.0.1
)

// Tagged without bumping Breaking: it reports itself as 2.20261009.
retract v0.3.20261009
