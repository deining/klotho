package aws_runtime

import (
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/klothoplatform/klotho/pkg/config"
	"github.com/klothoplatform/klotho/pkg/lang/python"
	"github.com/klothoplatform/klotho/pkg/runtime"

	"github.com/klothoplatform/klotho/pkg/core"
	"github.com/klothoplatform/klotho/pkg/provider/aws"
	"github.com/pkg/errors"
)

//go:generate ./compile_template.sh dispatcher_fargate dispatcher_lambda fs secret

//go:embed Fargate_Dockerfile.tmpl
var dockerfileFargate []byte

//go:embed Lambda_Dockerfile.tmpl
var dockerfileLambda []byte

//go:embed dispatcher_fargate.py.tmpl
var dispatcherFargate []byte

//go:embed dispatcher_lambda.py.tmpl
var dispatcherLambda []byte

//go:embed exec_fargate_requirements.txt
var execRequirementsFargate string

//go:embed exec_lambda_requirements.txt
var execRequirementsLambda string

//go:embed expose_requirements.txt
var exposeRequirements string

//go:embed persist_kv_requirements.txt
var kvRequirements string

//go:embed keyvalue.py
var kvRuntimeFiles embed.FS

//go:embed persist_fs_requirements.txt
var fsRequirements string

//go:embed fs.py.tmpl
var fsRuntimeFiles embed.FS

//go:embed persist_secret_requirements.txt
var secretRequirements string

//go:embed secret.py.tmpl
var secretRuntimeFiles embed.FS

//go:embed persist_orm_requirements.txt
var ormRequirements string

//go:embed proxy_eks.py
var proxyEksContents string

//go:embed proxy_fargate.py
var proxyFargateContents string

//go:embed proxy_lambda.py
var proxyLambdaContents string

type (
	AwsRuntime struct {
		TemplateConfig aws.TemplateConfig
		Cfg            *config.Application
	}

	TemplateData struct {
		aws.TemplateConfig
		ExecUnitName    string
		Expose          ExposeTemplateData
		ProjectFilePath string
	}

	ExposeTemplateData struct {
		ExportedAppVar string
		AppModule      string
	}
)

func (r *AwsRuntime) GetAppName() string {
	return r.TemplateConfig.AppName
}

func (r *AwsRuntime) AddExecRuntimeFiles(unit *core.ExecutionUnit, result *core.CompilationResult, deps *core.Dependencies) error {
	var dockerFile, dispatcher []byte
	var requirements string
	unitType := r.Cfg.GetResourceType(unit)
	switch unitType {
	case "lambda":
		dockerFile = dockerfileLambda
		dispatcher = dispatcherLambda
		requirements = execRequirementsLambda
	case "ecs", "eks", "apprunner":
		dockerFile = dockerfileFargate
		dispatcher = dispatcherFargate
		requirements = execRequirementsFargate
	default:
		return errors.Errorf("unsupported execution unit type: '%s'", unitType)
	}

	templateData := TemplateData{
		TemplateConfig: r.TemplateConfig,
		ExecUnitName:   unit.Name,
	}

	var err error

	if shouldAddExposeRuntimeFiles(unit, result, deps) {
		exposeData, err := getExposeTemplateData(unit, result, deps)
		if err != nil {
			return err
		}
		templateData.Expose = exposeData
		err = r.AddExposeRuntimeFiles(unit)
		if err != nil {
			return err
		}
	}

	reqTxtPath := ""
	for path, f := range unit.Files() {
		if filepath.Base(f.Path()) == "requirements.txt" {
			reqTxtPath = path
		}
	}
	if reqTxtPath == "" {
		return errors.Errorf("No `requirements.txt` found for execution unit %s", unit.Name)
	}
	templateData.ProjectFilePath = reqTxtPath
	if runtime.ShouldOverrideDockerfile(unit) {
		err = python.AddRuntimeFile(unit, templateData, "Dockerfile.tmpl", dockerFile)
		if err != nil {
			return err
		}
	}

	python.AddRequirements(unit, fsRequirements)
	proxyEnvVar := core.EnvironmentVariable{
		Name:       core.KLOTHO_PROXY_ENV_VAR_NAME,
		Kind:       core.InternalKind,
		ResourceID: core.KlothoPayloadName,
		Value:      string(core.BUCKET_NAME),
	}
	unit.EnvironmentVariables = append(unit.EnvironmentVariables, proxyEnvVar)
	err = r.AddFsRuntimeFiles(unit, proxyEnvVar.Name, "payload")
	if err != nil {
		return err
	}

	err = python.AddRuntimeFile(unit, templateData, "dispatcher.py.tmpl", dispatcher)
	if err != nil {
		return err
	}

	python.AddRequirements(unit, requirements)
	return nil
}

func shouldAddExposeRuntimeFiles(unit *core.ExecutionUnit, result *core.CompilationResult, deps *core.Dependencies) bool {
	for _, dep := range deps.Upstream(unit.Key()) {
		res := result.Get(dep)
		if _, ok := res.(*core.Gateway); ok {
			return true
		}
	}
	return false
}

// TODO: look into de-duplicating this function for reuse across languages
func getExposeTemplateData(unit *core.ExecutionUnit, result *core.CompilationResult, deps *core.Dependencies) (ExposeTemplateData, error) {
	upstreamGateways := core.FindUpstreamGateways(unit, result, deps)

	var sourceGateway *core.Gateway
	for _, gw := range upstreamGateways {
		if sourceGateway != nil && (sourceGateway.DefinedIn != gw.DefinedIn || sourceGateway.ExportVarName != gw.ExportVarName) {
			return ExposeTemplateData{},
				errors.Errorf("multiple gateways cannot target different web applications in the same execution unit: [%s -> %s],[%s -> %s]",
					gw.Name, unit.Name,
					sourceGateway.Name, unit.Name)
		}
		sourceGateway = gw
	}

	exposeData := ExposeTemplateData{}
	if sourceGateway != nil {
		exposeData.AppModule = strings.ReplaceAll(strings.TrimSuffix(sourceGateway.DefinedIn, ".py"), "/", ".")
		exposeData.ExportedAppVar = sourceGateway.ExportVarName
	}
	return exposeData, nil
}

func (r *AwsRuntime) AddExposeRuntimeFiles(unit *core.ExecutionUnit) error {
	python.AddRequirements(unit, exposeRequirements)
	return nil
}

func (r *AwsRuntime) GetKvRuntimeConfig() python.KVConfig {
	return python.KVConfig{
		Imports:                        "import klotho_runtime.keyvalue as __klotho_keyvalue",
		CacheClassArg:                  python.FunctionArg{Name: "cache_class", Value: "__klotho_keyvalue.KVStore"},
		AdditionalCacheConstructorArgs: []python.FunctionArg{{Name: "serializer", Value: "__klotho_keyvalue.DynamoDBSerializer()"}},
	}
}

func (r *AwsRuntime) GetFsRuntimeImportClass(id string, varName string) string {
	return fmt.Sprintf("import klotho_runtime.fs_%s as %s", id, varName)
}

func (r *AwsRuntime) GetSecretRuntimeImportClass(varName string) string {
	return fmt.Sprintf("import klotho_runtime.secret as %s", varName)
}

func (r *AwsRuntime) AddKvRuntimeFiles(unit *core.ExecutionUnit) error {
	python.AddRequirements(unit, kvRequirements)
	return r.AddRuntimeFiles(unit, kvRuntimeFiles)
}

type FsTemplateData struct {
	BucketNameEnvVar string
}

func (r *AwsRuntime) AddFsRuntimeFiles(unit *core.ExecutionUnit, envVarName string, id string) error {
	python.AddRequirements(unit, fsRequirements)
	templateData := FsTemplateData{BucketNameEnvVar: envVarName}
	content, err := fsRuntimeFiles.ReadFile("fs.py.tmpl")
	if err != nil {
		return err
	}
	err = python.AddRuntimeFile(unit, templateData, fmt.Sprintf("fs_%s.py.tmpl", id), content)
	return err
}

func (r *AwsRuntime) AddSecretRuntimeFiles(unit *core.ExecutionUnit) error {
	python.AddRequirements(unit, secretRequirements)
	return r.AddRuntimeFiles(unit, secretRuntimeFiles)
}

func (r *AwsRuntime) AddOrmRuntimeFiles(unit *core.ExecutionUnit) error {
	python.AddRequirements(unit, ormRequirements)
	return nil
}

func (r *AwsRuntime) AddProxyRuntimeFiles(unit *core.ExecutionUnit, proxyType string) error {
	var fileContents string
	switch proxyType {
	case "eks":
		fileContents = proxyEksContents
	case "ecs":
		fileContents = proxyFargateContents
	case "lambda":
		fileContents = proxyLambdaContents
	default:
		return errors.Errorf("unsupported exceution unit type: '%s'", r.Cfg.GetResourceType(unit))
	}
	err := r.AddRuntimeFile(unit, proxyType+"_proxy.py", []byte(fileContents))
	if err != nil {
		return err
	}
	// We also need to add the Fs files because exec to exec calls in aws use s3
	return r.AddRuntimeFiles(unit, fsRuntimeFiles)
}

func (r *AwsRuntime) AddRuntimeFiles(unit *core.ExecutionUnit, files embed.FS) error {
	templateData := TemplateData{
		TemplateConfig: r.TemplateConfig,
		ExecUnitName:   unit.Name,
	}
	err := python.AddRuntimeFiles(unit, files, templateData)
	return err
}

func (r *AwsRuntime) AddRuntimeFile(unit *core.ExecutionUnit, path string, content []byte) error {
	templateData := TemplateData{
		TemplateConfig: r.TemplateConfig,
		ExecUnitName:   unit.Name,
	}
	err := python.AddRuntimeFile(unit, templateData, path, content)
	return err
}
