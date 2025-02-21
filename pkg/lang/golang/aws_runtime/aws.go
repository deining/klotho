package aws_runtime

import (
	_ "embed"

	"github.com/klothoplatform/klotho/pkg/config"
	"github.com/klothoplatform/klotho/pkg/core"
	"github.com/klothoplatform/klotho/pkg/lang/golang"
	"github.com/klothoplatform/klotho/pkg/provider/aws"
	"github.com/klothoplatform/klotho/pkg/runtime"
	"github.com/pkg/errors"
)

type (
	AwsRuntime struct {
		TemplateConfig aws.TemplateConfig
		Cfg            *config.Application
	}

	TemplateData struct {
		aws.TemplateConfig
		ExecUnitName string
		Expose       ExposeTemplateData
		MainModule   string
	}

	ExposeTemplateData struct {
		ExportedAppVar string
		AppModule      string
	}
)

//go:embed Lambda_Dockerfile
var dockerfileLambda []byte

func (r *AwsRuntime) AddExecRuntimeFiles(unit *core.ExecutionUnit, result *core.CompilationResult, deps *core.Dependencies) error {
	var DockerFile []byte
	unitType := r.Cfg.GetResourceType(unit)
	switch unitType {
	case "lambda":
		DockerFile = dockerfileLambda
	default:
		return errors.Errorf("unsupported execution unit type: '%s'", unitType)
	}

	templateData := TemplateData{
		TemplateConfig: r.TemplateConfig,
		ExecUnitName:   unit.Name,
	}

	if runtime.ShouldOverrideDockerfile(unit) {
		err := golang.AddRuntimeFile(unit, templateData, "Dockerfile", DockerFile)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *AwsRuntime) GetFsImports() []golang.Import {
	return []golang.Import{
		{Package: "os"},
		{Package: "gocloud.dev/blob"},
		{Package: "gocloud.dev/blob/s3blob", Alias: "_"},
	}
}

func (r *AwsRuntime) GetSecretsImports() []golang.Import {
	return []golang.Import{
		{Package: "os"},
		{Package: "strings"},
		{Package: "gocloud.dev/runtimevar"},
		{Package: "gocloud.dev/runtimevar/awssecretsmanager", Alias: "_"},
	}
}

func (r *AwsRuntime) SetConfigType(id string, isSecret bool) {
	cfg := r.Cfg.Config[id]
	if cfg == nil {
		if isSecret {
			r.Cfg.Config[id] = &config.Config{Type: aws.Secrets_manager}
		}
	} else if isSecret && cfg.Type != aws.Secrets_manager {
		cfg.Type = aws.Secrets_manager
	}
}
