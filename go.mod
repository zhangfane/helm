module github.com/huolunl/helm/v3

go 1.16

require (
	cloud.google.com/go v0.81.0 // indirect
	github.com/Azure/go-autorest/autorest v0.11.19 // indirect
	github.com/BurntSushi/toml v0.3.1
	github.com/DATA-DOG/go-sqlmock v1.5.0
	github.com/Masterminds/semver/v3 v3.1.1
	github.com/Masterminds/sprig/v3 v3.2.2
	github.com/Masterminds/squirrel v1.5.0
	github.com/Masterminds/vcs v1.13.1
	github.com/asaskevich/govalidator v0.0.0-20200428143746-21a406dcc535
	github.com/containerd/containerd v1.4.11
	github.com/cyphar/filepath-securejoin v0.2.2
	github.com/deislabs/oras v0.11.1
	github.com/docker/distribution v2.7.1+incompatible
	github.com/docker/docker v17.12.0-ce-rc1.0.20200618181300-9dc6525e6118+incompatible
	github.com/docker/go-units v0.4.0
	github.com/evanphx/json-patch v4.9.0+incompatible
	github.com/fatih/color v1.9.0 // indirect
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32 // indirect
	github.com/gobwas/glob v0.2.3
	github.com/gofrs/flock v0.8.0
	github.com/google/uuid v1.2.0 // indirect
	github.com/gosuri/uitable v0.0.4
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/huolunl/helm-diff/v3 v3.0.0-20220516082011-51046feda4dd
	github.com/jmoiron/sqlx v1.3.1
	github.com/lib/pq v1.10.0
	github.com/mattn/go-colorable v0.1.8 // indirect
	github.com/mattn/go-runewidth v0.0.8 // indirect
	github.com/mattn/go-shellwords v1.0.11
	github.com/mitchellh/copystructure v1.1.1
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.0.2
	github.com/phayes/freeport v0.0.0-20180830031419-95f893ade6f2
	github.com/pkg/errors v0.9.1
	github.com/rubenv/sql-migrate v0.0.0-20200616145509-8d140a17f351
	github.com/sirupsen/logrus v1.8.1
	github.com/spf13/cobra v1.1.3
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0
	github.com/xeipuuv/gojsonschema v1.2.0
	golang.org/x/crypto v0.0.0-20210616213533-5ff15b29337e
	golang.org/x/net v0.0.0-20210614182718-04defd469f4e // indirect
	golang.org/x/oauth2 v0.0.0-20210413134643-5e61552d6c78 // indirect
	golang.org/x/term v0.0.0-20210220032956-6a3ed077a48d
	google.golang.org/genproto v0.0.0-20210420162539-3c870d7478d2 // indirect
	google.golang.org/grpc v1.37.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v2 v2.4.0
	gopkg.in/yaml.v3 v3.0.0-20210107172259-749611fa9fcc // indirect
	k8s.io/api v0.21.0
	k8s.io/apiextensions-apiserver v0.21.0
	k8s.io/apimachinery v0.21.0
	k8s.io/apiserver v0.21.0
	k8s.io/cli-runtime v0.21.0
	k8s.io/client-go v0.21.0
	k8s.io/klog/v2 v2.8.0
	k8s.io/kubectl v0.21.0
	sigs.k8s.io/yaml v1.2.0
)

replace github.com/docker/distribution => github.com/docker/distribution v0.0.0-20191216044856-a8371794149d
