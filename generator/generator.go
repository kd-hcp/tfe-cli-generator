package generator

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	log "github.com/sirupsen/logrus"
)

// Summary holds information about the generation process.
type Summary struct {
	ServicesProcessed int
	MethodsProcessed  int
	GeneratedFiles    []string
	Errors            []string
}

// CommandData holds the data needed to generate a command file.
type CommandData struct {
	CommandName          string
	Use                  string
	Short                string
	ServiceName          string
	Parameters           []Parameter
	Returns              []Return
	NeedsOptions         bool
	OptionsType          string
	OptionsFields        []OptionsField
	HasResult            bool
	HasError             bool
	Imports              map[string]bool
	GeneratedCommandFile string
	ServiceDescription   string
	CommandDescription   string
}

// Parameter represents a function parameter.
type Parameter struct {
	Name string
	Type string
	IsIO bool // Indicates if the parameter type starts with "io."
}

// Return represents a function return value.
type Return struct {
	Name string
	Type string
}

// OptionsField represents a field within an Options struct.
type OptionsField struct {
	Name        string
	Type        string
	Description string // Optional: For generating flag descriptions
}

// prefixType intelligently prefixes a type with "tfe." if it's non-basic and lacks a package prefix.
// It correctly handles composite types like slices, pointers, maps, and channels.
func prefixType(typeStr string) string {
	// Handle pointer
	if strings.HasPrefix(typeStr, "*") {
		return "*" + prefixType(strings.TrimPrefix(typeStr, "*"))
	}
	// Handle slice
	if strings.HasPrefix(typeStr, "[]") {
		return "[]" + prefixType(strings.TrimPrefix(typeStr, "[]"))
	}
	// Handle map
	if strings.HasPrefix(typeStr, "map[") {
		endIdx := strings.Index(typeStr, "]")
		keyType := typeStr[4:endIdx]
		valueType := typeStr[endIdx+1:]
		return fmt.Sprintf("map[%s]%s", prefixType(keyType), prefixType(valueType))
	}
	// Handle channels
	if strings.HasPrefix(typeStr, "chan ") {
		return "chan " + prefixType(strings.TrimPrefix(typeStr, "chan "))
	}
	if strings.HasPrefix(typeStr, "<-chan ") {
		return "<-chan " + prefixType(strings.TrimPrefix(typeStr, "<-chan "))
	}
	if strings.HasPrefix(typeStr, "chan<- ") {
		return "chan<- " + prefixType(strings.TrimPrefix(typeStr, "chan<- "))
	}

	// Now, check if it's a basic type or already has package prefix
	if strings.Contains(typeStr, ".") || isBasicType(typeStr) || typeStr == "error" {
		return typeStr
	}
	// Prefix with tfe.
	return "tfe." + typeStr
}

// getBaseType extracts the base type name from a qualified type string.
func getBaseType(typeStr string) string {
	typeStr = strings.TrimPrefix(typeStr, "*")
	parts := strings.Split(typeStr, ".")
	return parts[len(parts)-1]
}

// Generate scans the go-tfe library and generates the CLI code.
func Generate(goTfePath string) error {
	log.Debug("Starting code generation process")

	// Initialize the summary
	summary := &Summary{}

	// Prepare the output directory
	outputDir := "../tfe-cli"
	err := prepareOutputDir(outputDir)
	if err != nil {
		summary.Errors = append(summary.Errors, err.Error())
		return err
	}

	// Generate the root command and main.go
	err = generateRootCommand(outputDir)
	if err != nil {
		summary.Errors = append(summary.Errors, err.Error())
		return err
	}
	summary.GeneratedFiles = append(summary.GeneratedFiles, filepath.Join(outputDir, "main.go"))
	summary.GeneratedFiles = append(summary.GeneratedFiles, filepath.Join(outputDir, "cmd", "root.go"))

	// Get all service interfaces from Client struct recursively
	serviceTypes, err := getAllServiceTypes(goTfePath)
	if err != nil {
		summary.Errors = append(summary.Errors, err.Error())
		return err
	}
	summary.ServicesProcessed = len(serviceTypes)

	if summary.ServicesProcessed == 0 {
		log.Warn("No service types found in Client struct.")
	}

	// Get methods for each service interface
	serviceMethods, err := getServiceMethods(goTfePath, serviceTypes)
	if err != nil {
		summary.Errors = append(summary.Errors, err.Error())
		return err
	}

	// Process methods
	for structName, interfaceName := range serviceTypes {
		methods, exists := serviceMethods[structName]
		if !exists || len(methods) == 0 {
			log.Warnf("No methods found for service struct %s implementing interface %s", structName, interfaceName)
			continue
		}
		log.Infof("Processing service struct: %s implementing interface: %s", structName, interfaceName)
		for _, method := range methods {
			err := processMethod(goTfePath, structName, interfaceName, method, outputDir, summary)
			if err != nil {
				log.Errorf("Error processing method %s.%s: %v", structName, method.Name.Name, err)
				summary.Errors = append(summary.Errors, fmt.Sprintf("Error processing method %s.%s: %v", structName, method.Name.Name, err))
			} else {
				summary.MethodsProcessed++
			}
		}
	}

	log.Info("Code generation completed successfully")
	printSummary(summary)

	return nil
}

// getAllServiceTypes extracts all service types from the Client struct and recursively from nested services.
func getAllServiceTypes(goTfePath string) (map[string]string, error) {
	serviceTypes := make(map[string]string)
	tfeFilePath := filepath.Join(goTfePath, "tfe.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, tfeFilePath, nil, parser.ParseComments)
	if err != nil {
		log.Errorf("Failed to parse file %s: %v", tfeFilePath, err)
		return nil, fmt.Errorf("failed to parse file %s: %v", tfeFilePath, err)
	}

	// Find the Client struct
	var clientStruct *ast.StructType
	for _, decl := range file.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok && genDecl.Tok == token.TYPE {
			for _, spec := range genDecl.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok && typeSpec.Name.Name == "Client" {
					if structType, ok := typeSpec.Type.(*ast.StructType); ok {
						clientStruct = structType
						break
					}
				}
			}
		}
	}

	if clientStruct == nil {
		return nil, fmt.Errorf("Client struct not found in tfe.go")
	}

	// Define a list of known non-service fields to exclude
	nonServiceFields := map[string]bool{
		"baseURL":           true,
		"registryBaseURL":   true,
		"token":             true,
		"headers":           true,
		"http":              true,
		"limiter":           true,
		"retryLogHook":      true,
		"retryServerErrors": true,
		"remoteAPIVersion":  true,
		"remoteTFEVersion":  true,
		"appName":           true,
	}

	// Recursively traverse service fields
	var traverseService func(*ast.StructType) error
	traverseService = func(structType *ast.StructType) error {
		for _, field := range structType.Fields.List {
			if len(field.Names) == 0 {
				continue
			}
			fieldName := field.Names[0].Name

			// Skip known non-service fields
			if nonServiceFields[fieldName] {
				log.Debugf("Skipping non-service field: %s", fieldName)
				continue
			}

			fieldType := exprToString(field.Type)
			baseType := getBaseType(fieldType)

			// Ensure the baseType is exported
			if !isExported(baseType) {
				log.Debugf("Skipping unexported service field: %s of type %s", fieldName, baseType)
				continue
			}

			// Assume the implementing struct is the lowercase version of the interface
			structName := strings.ToLower(baseType)

			// Add to serviceTypes map if not already present
			if _, exists := serviceTypes[structName]; !exists {
				serviceTypes[structName] = baseType // Mapping structName -> interfaceName
				log.Infof("Identified service struct: %s implementing interface: %s", structName, baseType)

				// Now, find nested services within this service struct
				nestedStruct, err := findStruct(goTfePath, structName)
				if err != nil {
					log.Warnf("Could not find struct for service type %s: %v", structName, err)
					continue
				}
				if nestedStruct != nil {
					err = traverseService(nestedStruct)
					if err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	err = traverseService(clientStruct)
	if err != nil {
		return nil, err
	}

	log.Infof("Found %d service struct(s)", len(serviceTypes))
	return serviceTypes, nil
}

// findStruct locates the struct type with the given name in the go-tfe package.
func findStruct(goTfePath, structName string) (*ast.StructType, error) {
	var structType *ast.StructType

	err := filepath.Walk(goTfePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Errorf("Error accessing path %s: %v", path, err)
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") || strings.HasSuffix(info.Name(), "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			log.Errorf("Failed to parse file %s: %v", path, err)
			return fmt.Errorf("failed to parse file %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != structName {
					continue
				}
				sType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				structType = sType
				return filepath.SkipDir
			}
		}

		return nil
	})

	if err != nil && err != filepath.SkipDir {
		return nil, err
	}

	return structType, nil
}

// getServiceMethods collects methods from service structs.
func getServiceMethods(goTfePath string, serviceTypes map[string]string) (map[string][]*ast.FuncDecl, error) {
	serviceMethods := make(map[string][]*ast.FuncDecl)

	// Collect methods from the parsed files
	err := filepath.Walk(goTfePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Errorf("Error accessing path %s: %v", path, err)
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") || strings.HasSuffix(info.Name(), "_test.go") {
			return nil
		}

		log.Debugf("Parsing file for methods: %s", path)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			log.Errorf("Failed to parse file %s: %v", path, err)
			return fmt.Errorf("failed to parse file %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
				continue
			}

			// Skip unexported methods
			if !isExported(funcDecl.Name.Name) {
				log.Debugf("Skipping unexported method: %s", funcDecl.Name.Name)
				continue
			}

			recvTypeExpr := funcDecl.Recv.List[0].Type
			recvType := exprToString(recvTypeExpr)
			recvBaseType := getBaseType(recvType)

			// Handle pointer receivers
			if strings.HasPrefix(recvBaseType, "*") {
				recvBaseType = strings.TrimPrefix(recvBaseType, "*")
			}

			// Check if the receiver struct is in serviceTypes
			if interfaceName, exists := serviceTypes[recvBaseType]; exists {
				serviceMethods[recvBaseType] = append(serviceMethods[recvBaseType], funcDecl)
				log.Debugf("Found method %s for service struct %s implementing interface %s", funcDecl.Name.Name, recvBaseType, interfaceName)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	log.Infof("Collected methods for %d service struct(s)", len(serviceMethods))
	return serviceMethods, nil
}

// processMethod processes each method to generate a command file.
func processMethod(goTfePath string, structName string, interfaceName string, method *ast.FuncDecl, outputDir string, summary *Summary) error {
	methodName := method.Name.Name
	log.Infof("Processing method: %s.%s", structName, methodName)

	funcType := method.Type

	params, optionsParam := extractParametersFromFuncType(funcType)
	returns := extractReturnsFromFuncType(funcType)

	// Determine if the method has a result or error
	hasResult := hasResult(returns)
	hasError := hasError(returns)

	// Determine if options are needed
	needsOptions := optionsParam != nil
	optionsType := ""
	optionsFields := []OptionsField{}

	imports := map[string]bool{
		"fmt":                    true,
		"github.com/spf13/cobra": true,
	}

	if needsOptions {
		optionsType = strings.TrimPrefix(optionsParam.Type, "*tfe.")
		optionsFields, _ = getOptionsFields(goTfePath, optionsType)
		imports["github.com/hashicorp/go-tfe"] = true
	}

	for _, param := range params {
		if param.IsIO {
			imports["os"] = true
			imports["io"] = true
		}
	}

	for _, ret := range returns {
		if ret.Type == "io.Reader" || ret.Type == "io.Writer" {
			imports["io"] = true
		}
	}

	commandData := CommandData{
		CommandName:          strings.ToLower(interfaceName + "_" + methodName),
		Use:                  methodName,
		Short:                fmt.Sprintf("Execute the %s.%s API method", interfaceName, methodName),
		ServiceName:          interfaceName,
		Parameters:           params,
		Returns:              returns,
		NeedsOptions:         needsOptions,
		OptionsType:          optionsType,
		OptionsFields:        optionsFields,
		HasResult:            hasResult,
		HasError:             hasError,
		Imports:              imports,
		GeneratedCommandFile: filepath.Join(outputDir, "cmd", strings.ToLower(interfaceName+"_"+methodName)+".go"),
	}

	err := generateCommandFile(commandData, outputDir)
	if err != nil {
		log.Errorf("Error generating command file for method %s.%s: %v", interfaceName, methodName, err)
		return err
	}

	summary.GeneratedFiles = append(summary.GeneratedFiles, commandData.GeneratedCommandFile)
	log.Infof("Generated command for method: %s.%s", interfaceName, methodName)
	return nil
}

// extractParametersFromFuncType extracts parameters from a function type.
func extractParametersFromFuncType(funcType *ast.FuncType) (params []Parameter, optionsParam *Parameter) {
	paramIndex := 0
	for _, field := range funcType.Params.List {
		paramType := exprToString(field.Type)
		paramType = prefixType(paramType)

		// Determine if the parameter type starts with "io."
		isIO := strings.HasPrefix(paramType, "io.")

		paramNames := field.Names
		if len(paramNames) == 0 {
			// Generate a parameter name
			paramName := fmt.Sprintf("param%d", paramIndex)
			paramIndex++
			paramNames = []*ast.Ident{{Name: paramName}}
		}
		for _, name := range paramNames {
			paramName := name.Name
			if strings.HasPrefix(paramType, "*tfe.") && strings.HasSuffix(paramType, "Options") {
				optionsParam = &Parameter{
					Name: paramName,
					Type: paramType,
					IsIO: false, // Options types are not IO types
				}
			} else {
				params = append(params, Parameter{
					Name: paramName,
					Type: paramType,
					IsIO: isIO,
				})
			}
		}
	}
	return params, optionsParam
}

// extractReturnsFromFuncType extracts return values from a function type.
func extractReturnsFromFuncType(funcType *ast.FuncType) []Return {
	var returns []Return
	if funcType.Results == nil {
		return returns
	}
	for i, field := range funcType.Results.List {
		returnType := exprToString(field.Type)
		returnType = prefixType(returnType)
		returnName := ""
		if len(field.Names) > 0 {
			returnName = field.Names[0].Name
		} else {
			returnName = fmt.Sprintf("ret%d", i)
		}
		returns = append(returns, Return{
			Name: returnName,
			Type: returnType,
		})
	}
	return returns
}

// hasResult checks if the command has a result return.
func hasResult(returns []Return) bool {
	for _, ret := range returns {
		if ret.Type != "error" {
			return true
		}
	}
	return false
}

// hasError checks if the command has an error return.
func hasError(returns []Return) bool {
	for _, ret := range returns {
		if ret.Type == "error" {
			return true
		}
	}
	return false
}

// generateCommandFile generates a command file from a template.
func generateCommandFile(data CommandData, outputDir string) error {
	tmplPath := "./templates/command.go.tmpl"
	outputPath := filepath.Join(outputDir, "cmd", data.CommandName+".go")
	log.Debugf("Generating command file: %s", outputPath)
	return generateFileFromTemplate(tmplPath, outputPath, data)
}

// generateRootCommand generates the root command and main.go.
func generateRootCommand(outputDir string) error {
	log.Debug("Generating main.go and root command files")

	mainTemplatePath := "./templates/main.go.tmpl"
	mainOutputPath := filepath.Join(outputDir, "main.go")
	err := generateFileFromTemplate(mainTemplatePath, mainOutputPath, nil)
	if err != nil {
		return err
	}
	log.Infof("Successfully generated file: %s", mainOutputPath)

	rootTemplatePath := "./templates/root.go.tmpl"
	rootOutputPath := filepath.Join(outputDir, "cmd", "root.go")
	err = generateFileFromTemplate(rootTemplatePath, rootOutputPath, nil)
	if err != nil {
		return err
	}
	log.Infof("Successfully generated file: %s", rootOutputPath)

	log.Info("Successfully generated main.go and root.go")
	return nil
}

// prepareOutputDir prepares the output directory.
func prepareOutputDir(outputDir string) error {
	cmdDir := filepath.Join(outputDir, "cmd")
	if err := os.RemoveAll(cmdDir); err != nil {
		return fmt.Errorf("failed to clean cmd directory: %v", err)
	}
	if err := os.MkdirAll(cmdDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create cmd directory: %v", err)
	}
	return nil
}

// generateFileFromTemplate generates a file from a template.
func generateFileFromTemplate(templatePath, outputPath string, data interface{}) error {
	funcMap := template.FuncMap{
		"stringsToLower":         strings.ToLower,
		"stringsTrimPrefix":      strings.TrimPrefix,
		"stringsTitle":           strings.Title,
		"GenerateImports":        generateImports,
		"GenerateFlags":          generateFlags,
		"GenerateOptionsParsing": generateOptionsParsing,
		"ToTitleCase":            toTitleCase,
		"defaultValue":           defaultValue,
		"stripSlicePrefix":       stripSlicePrefix,
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Funcs(funcMap).ParseFiles(templatePath)
	if err != nil {
		return fmt.Errorf("failed to parse template %s: %v", templatePath, err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %v", outputPath, err)
	}
	defer f.Close()

	err = tmpl.Execute(f, data)
	if err != nil {
		return fmt.Errorf("failed to execute template: %v", err)
	}

	log.Debugf("Successfully generated file: %s", outputPath)
	return nil
}
func stripSlicePrefix(typeStr string) string {
	return strings.TrimPrefix(typeStr, "[]")
}
func defaultValue(typ string) string {
	switch typ {
	case "string":
		return `""`
	case "int", "int8", "int16", "int32", "int64":
		return "0"
	case "bool":
		return "false"
	case "float32", "float64":
		return "0.0"
	case "io.Reader", "io.Writer":
		return "nil"
	default:
		// Fallback for other complex types (like structs, slices, etc.)
		return "nil"
	}
}

// getOptionsFields extracts fields of a given Options struct.
func getOptionsFields(goTfePath, optionsTypeName string) ([]OptionsField, error) {
	var fields []OptionsField

	err := filepath.Walk(goTfePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") || strings.HasSuffix(info.Name(), "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("failed to parse file %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != optionsTypeName {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structType.Fields.List {
					if len(field.Names) == 0 {
						continue
					}
					fieldName := field.Names[0].Name
					fieldType := exprToString(field.Type)
					fieldType = prefixType(fieldType)

					description := ""
					if field.Doc != nil && len(field.Doc.List) > 0 {
						description = strings.TrimSpace(strings.TrimPrefix(field.Doc.List[0].Text, "//"))
					} else if field.Comment != nil && len(field.Comment.List) > 0 {
						description = strings.TrimSpace(strings.TrimPrefix(field.Comment.List[0].Text, "//"))
					}

					fields = append(fields, OptionsField{
						Name:        fieldName,
						Type:        fieldType,
						Description: description,
					})
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return fields, nil
}

// exprToString converts an expression to its string representation.
func exprToString(expr ast.Expr) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, token.NewFileSet(), expr)
	return buf.String()
}

// isExported checks if a name is exported.
func isExported(name string) bool {
	return ast.IsExported(name)
}

// isBasicType checks if a type is a basic Go type.
func isBasicType(t string) bool {
	basicTypes := map[string]bool{
		"bool":       true,
		"string":     true,
		"int":        true,
		"int8":       true,
		"int16":      true,
		"int32":      true,
		"int64":      true,
		"uint":       true,
		"uint8":      true,
		"uint16":     true,
		"uint32":     true,
		"uint64":     true,
		"uintptr":    true,
		"byte":       true,
		"rune":       true,
		"float32":    true,
		"float64":    true,
		"complex64":  true,
		"complex128": true,
		"error":      true,
	}
	return basicTypes[t]
}

// generateImports dynamically collects the imports based on CommandData fields.
func generateImports(data CommandData) []string {
	imports := make(map[string]bool)

	// Base imports
	imports["context"] = true
	imports["log"] = true
	imports["fmt"] = true

	// Dynamically add required imports based on parameters and returns
	for _, param := range data.Parameters {
		// Add "io" import if the type involves io.Reader or io.Writer
		if param.Type == "io.Reader" || param.Type == "io.Writer" {
			imports["io"] = true
		}
		// Add "os" import if needed for io.Writer cases like os.Stdout
		if param.IsIO {
			imports["os"] = true
		}
		if strings.HasPrefix(param.Type, "tfe.") || strings.HasPrefix(param.Type, "*tfe.") {
			imports["github.com/hashicorp/go-tfe"] = true
		}
	}

	for _, ret := range data.Returns {
		if ret.Type == "io.Reader" || ret.Type == "io.Writer" {
			imports["io"] = true
		}
		if strings.HasPrefix(ret.Type, "tfe.") || strings.HasPrefix(ret.Type, "*tfe.") {
			imports["github.com/hashicorp/go-tfe"] = true
		}
	}

	// Add "github.com/spf13/cobra" for cobra command usage
	imports["github.com/spf13/cobra"] = true

	// Add tfe import if needed
	if data.NeedsOptions || len(data.OptionsFields) > 0 {
		imports["github.com/hashicorp/go-tfe"] = true
	}

	// Convert map to slice of strings
	importList := []string{}
	for imp := range imports {
		importList = append(importList, imp)
	}

	return importList
}

// generateFlags generates the flag definitions based on the parameters.
func generateFlags(data CommandData) string {
	var flags []string
	for _, param := range data.Parameters {
		flagName := toSnakeCase(param.Name)
		switch param.Type {
		case "string":
			flags = append(flags, fmt.Sprintf(`cmd.Flags().String("%s", "", "Description for %s")`, flagName, param.Name))
		case "int":
			flags = append(flags, fmt.Sprintf(`cmd.Flags().Int("%s", 0, "Description for %s")`, flagName, param.Name))
		case "bool":
			flags = append(flags, fmt.Sprintf(`cmd.Flags().Bool("%s", false, "Description for %s")`, flagName, param.Name))
		case "io.Reader", "io.Writer":
			flags = append(flags, fmt.Sprintf(`cmd.Flags().String("%s", "", "Description for %s (expects file path)")`, flagName, param.Name))
		default:
			flags = append(flags, fmt.Sprintf(`// TODO: Add flag for %s (%s)`, param.Name, param.Type))
		}
	}
	return strings.Join(flags, "\n\t")
}

// generateOptionsParsing generates the code to parse options from flags.
func generateOptionsParsing(data CommandData) string {
	if !data.NeedsOptions {
		return ""
	}
	var parsing []string
	for _, field := range data.OptionsFields {
		flagName := toSnakeCase(field.Name)
		switch field.Type {
		case "string":
			parsing = append(parsing, fmt.Sprintf(`options.%s, _ = cmd.Flags().GetString("%s")`, field.Name, flagName))
		case "int":
			parsing = append(parsing, fmt.Sprintf(`options.%s, _ = cmd.Flags().GetInt("%s")`, field.Name, flagName))
		case "bool":
			parsing = append(parsing, fmt.Sprintf(`options.%s, _ = cmd.Flags().GetBool("%s")`, field.Name, flagName))
		default:
			parsing = append(parsing, fmt.Sprintf(`// TODO: Parse flag for %s (%s)`, field.Name, field.Type))
		}
	}
	return strings.Join(parsing, "\n\t")
}

// toSnakeCase converts a CamelCase string to snake_case.
func toSnakeCase(s string) string {
	var result []rune
	for i, r := range s {
		if i > 0 && isUpper(r) && (isLower(rune(s[i-1])) || (i+1 < len(s) && isLower(rune(s[i+1])))) {
			result = append(result, '_')
		}
		result = append(result, toLower(r))
	}
	return string(result)
}

// isUpper checks if a rune is uppercase.
func isUpper(r rune) bool {
	return 'A' <= r && r <= 'Z'
}

// isLower checks if a rune is lowercase.
func isLower(r rune) bool {
	return 'a' <= r && r <= 'z'
}

// toLower converts a rune to lowercase.
func toLower(r rune) rune {
	if isUpper(r) {
		return r + ('a' - 'A')
	}
	return r
}

// toTitleCase converts a string to Title Case.
func toTitleCase(s string) string {
	return strings.Title(s)
}

// printSummary prints a summary of the code generation process.
func printSummary(summary *Summary) {
	fmt.Println("\nCode Generation Summary:")
	fmt.Println("========================")
	fmt.Printf("Services Processed: %d\n", summary.ServicesProcessed)
	fmt.Printf("Methods Processed:  %d\n", summary.MethodsProcessed)
	fmt.Printf("\nGenerated Files:\n")
	for _, file := range summary.GeneratedFiles {
		fmt.Printf("  - %s\n", file)
	}
	if len(summary.Errors) > 0 {
		fmt.Printf("\nErrors Encountered:\n")
		for _, err := range summary.Errors {
			fmt.Printf("  - %s\n", err)
		}
	} else {
		fmt.Println("\nNo errors encountered.")
	}
	fmt.Println("========================")
}
