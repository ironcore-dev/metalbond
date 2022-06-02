module github.com/onmetal/metalbond

go 1.18

require (
	github.com/alecthomas/kong v0.5.0
	github.com/google/addlicense v1.0.0
	github.com/sirupsen/logrus v1.8.1
	github.com/vishvananda/netlink v1.1.0
	google.golang.org/protobuf v1.25.0
	gopkg.in/yaml.v2 v2.4.0
)

require (
	github.com/bmatcuk/doublestar/v4 v4.0.2 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/vishvananda/netns v0.0.0-20200728191858-db3c7e526aae // indirect
	golang.org/x/sync v0.0.0-20220513210516-0976fa681c29 // indirect
	golang.org/x/sys v0.0.0-20220517195934-5e4e11fc645e // indirect
	golang.org/x/xerrors v0.0.0-20220517211312-f3a8303e98df // indirect
)

replace github.com/vishvananda/netlink v1.1.0 => github.com/byteocean/netlink v1.1.1-0.20220422134219-54ec8c328844
