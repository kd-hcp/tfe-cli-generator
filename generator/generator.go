// generator/generator.go

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
	NeedsTFEImport       bool
	NeedsIOImport        bool
	HasResult            bool
	HasError             bool
	AdditionalImports    []string
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
		// Find the closing ']'
		endIdx := strings.Index(typeStr, "]")
		if endIdx == -1 {
			// Invalid map type
			return typeStr
		}
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
	// Remove pointer if present
	typeStr = strings.TrimPrefix(typeStr, "*")
	// Remove package prefix
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

	// Print the summary log
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

			// **New Check:** Ensure the baseType is exported
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

			// **New Check:** Skip unexported methods
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

	// Verify if the method exists in the go-tfe package (redundant since we already filtered unexported)
	// Optional: Additional checks can be added here

	// method.Type is already *ast.FuncType; no need for type assertion
	funcType := method.Type

	params, optionsParam := extractParametersFromFuncType(funcType)
	returns := extractReturnsFromFuncType(funcType)

	log.Debugf("Extracted %d parameter(s) for method %s.%s", len(params), structName, methodName)
	for _, p := range params {
		log.Debugf("Parameter: %s %s", p.Name, p.Type)
	}
	log.Debugf("Extracted %d return value(s) for method %s.%s", len(returns), structName, methodName)
	for _, r := range returns {
		log.Debugf("Return: %s %s", r.Name, r.Type)
	}

	// Determine return types
	hasResult := false
	hasError := false
	for _, ret := range returns {
		if ret.Type != "error" && !strings.HasPrefix(ret.Type, "error") {
			hasResult = true
		} else if ret.Type == "error" {
			hasError = true
		}
	}

	// Determine if options are needed
	needsOptions := optionsParam != nil
	optionsType := ""
	optionsFields := []OptionsField{}
	needsTFEImport := false
	needsIOImport := false
	additionalImports := []string{}

	if needsOptions {
		// Remove the "*tfe." prefix to get the base type name
		optionsType = strings.TrimPrefix(optionsParam.Type, "*tfe.")
		needsTFEImport = true

		// Extract fields from the Options struct
		optionsFields, err := getOptionsFields(goTfePath, optionsType)
		if err != nil {
			log.Errorf("Error extracting fields from Options struct %s: %v", optionsType, err)
			summary.Errors = append(summary.Errors, fmt.Sprintf("Error extracting fields from Options struct %s: %v", optionsType, err))
			return err
		}

		log.Debugf("Processed OptionsType: %s with %d field(s)", optionsType, len(optionsFields))
	}

	// Check if any parameter types require tfe or io import
	for _, param := range params {
		if strings.HasPrefix(param.Type, "tfe.") {
			needsTFEImport = true
		}
		if param.IsIO {
			needsIOImport = true
		}
	}

	// Check if return types require tfe or io import
	for _, ret := range returns {
		if strings.HasPrefix(ret.Type, "*tfe.") || strings.HasPrefix(ret.Type, "tfe.") {
			needsTFEImport = true
		}
		if strings.Contains(ret.Type, "io.") {
			needsIOImport = true
		}
	}

	// Collect additional imports based on parameter and return types
	if needsTFEImport {
		additionalImports = append(additionalImports, `"github.com/hashicorp/go-tfe"`)
	}
	if needsIOImport {
		additionalImports = append(additionalImports, `"io"`)
	}

	commandData := CommandData{
		CommandName:          strings.ToLower(interfaceName + "_" + methodName), // Use interfaceName
		Use:                  methodName,
		Short:                fmt.Sprintf("Execute the %s.%s API method", interfaceName, methodName),
		ServiceName:          interfaceName, // Use interfaceName instead of structName
		Parameters:           params,
		Returns:              returns,
		NeedsOptions:         needsOptions,
		OptionsType:          optionsType,
		OptionsFields:        optionsFields,
		NeedsTFEImport:       needsTFEImport,
		NeedsIOImport:        needsIOImport,
		HasResult:            hasResult,
		HasError:             hasError,
		AdditionalImports:    additionalImports,
		GeneratedCommandFile: filepath.Join(outputDir, "cmd", strings.ToLower(interfaceName+"_"+methodName)+".go"),
		ServiceDescription:   fmt.Sprintf("%s service commands.", interfaceName),
		CommandDescription:   fmt.Sprintf("Command to execute the %s.%s API method.", interfaceName, methodName),
	}

	err := generateCommandFile(commandData, outputDir)
	if err != nil {
		log.Errorf("Error generating command file for method %s.%s: %v", interfaceName, methodName, err)
		return fmt.Errorf("error generating command file: %v", err)
	}

	// Add the generated file to the summary
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
		if paramType == "context.Context" {
			// Skip context parameter
			continue
		}
		paramNames := field.Names
		if len(paramNames) == 0 {
			// Generate a parameter name
			paramName := fmt.Sprintf("param%d", paramIndex)
			paramIndex++
			paramNames = []*ast.Ident{{Name: paramName}}
		}
		for _, name := range paramNames {
			paramName := name.Name
			// Identify if this parameter is an Options type
			if strings.HasPrefix(paramType, "*tfe.") && strings.HasSuffix(paramType, "Options") {
				optionsParam = &Parameter{
					Name: paramName,
					Type: paramType,
					IsIO: false, // Options types are not IO types
				}
			} else {
				isIO := strings.HasPrefix(paramType, "io.")
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

// getOptionsFields parses the `go-tfe` package and extracts fields of a given Options struct.
func getOptionsFields(goTfePath, optionsTypeName string) ([]OptionsField, error) {
	var fields []OptionsField

	// Traverse all .go files in goTfePath to find the OptionsType struct
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

		// Traverse the AST to find the Options struct
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
					// Assuming each field has a name
					if len(field.Names) == 0 {
						continue
					}
					fieldName := field.Names[0].Name
					fieldType := exprToString(field.Type)
					fieldType = prefixType(fieldType)

					// Optional: Extract field tags or comments for descriptions
					description := ""
					if field.Doc != nil && len(field.Doc.List) > 0 {
						// Assuming the first comment line is the description
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

	if len(fields) == 0 {
		return nil, fmt.Errorf("Options struct %s not found in go-tfe package", optionsTypeName)
	}

	log.Infof("Extracted %d field(s) from Options struct %s", len(fields), optionsTypeName)
	return fields, nil
}

// exprToString converts an expression to its string representation.
func exprToString(expr ast.Expr) string {
	var buf bytes.Buffer
	err := printer.Fprint(&buf, token.NewFileSet(), expr)
	if err != nil {
		return ""
	}
	return buf.String()
}

// isExported checks if a name is exported.
func isExported(name string) bool {
	return ast.IsExported(name)
}

// isBasicType checks if a type is a basic Go type.
func isBasicType(t string) bool {
	basicTypes := map[string]bool{
		// Primitive types
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
		"byte":       true, // Alias for uint8
		"rune":       true, // Alias for int32
		"float32":    true,
		"float64":    true,
		"complex64":  true,
		"complex128": true,
		"error":      true,
		// Add more built-in types if necessary
	}
	return basicTypes[t]
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
	log.Debugf("Preparing output directory: %s", outputDir)
	cmdDir := filepath.Join(outputDir, "cmd")
	if err := os.RemoveAll(cmdDir); err != nil {
		log.Errorf("Failed to clean cmd directory: %v", err)
		return fmt.Errorf("failed to clean cmd directory: %v", err)
	}
	if err := os.MkdirAll(cmdDir, os.ModePerm); err != nil {
		log.Errorf("Failed to create cmd directory: %v", err)
		return fmt.Errorf("failed to create cmd directory: %v", err)
	}
	log.Debugf("Created cmd directory: %s", cmdDir)
	return nil
}

// generateFileFromTemplate generates a file from a template.
func generateFileFromTemplate(templatePath, outputPath string, data interface{}) error {
	// Define the FuncMap with necessary functions
	funcMap := template.FuncMap{
		"stringsNewReader":       strings.NewReader,
		"stringsToLower":         strings.ToLower,
		"stringsHasPrefix":       strings.HasPrefix,
		"stringsHasSuffix":       strings.HasSuffix,
		"stringsReplace":         strings.Replace,
		"stringsSplit":           strings.Split,
		"stringsJoin":            strings.Join,
		"stringsContains":        strings.Contains,
		"stringsTrimSpace":       strings.TrimSpace,
		"stringsTitle":           strings.Title,
		"stringsToUpper":         strings.ToUpper,
		"stringsTrimPrefix":      strings.TrimPrefix,
		"stringsTrimSuffix":      strings.TrimSuffix,
		"stringsIndex":           strings.Index,
		"stringsLastIndex":       strings.LastIndex,
		"stringsRepeat":          strings.Repeat,
		"stringsCount":           strings.Count,
		"stringsMap":             strings.Map,
		"stringsFields":          strings.Fields,
		"stringsFieldsFunc":      strings.FieldsFunc,
		"stringsReplaceAll":      strings.ReplaceAll,
		"ToCamelCase":            toCamelCase,
		"ToSnakeCase":            toSnakeCase,
		"ToTitleCase":            toTitleCase,
		"IsBasicType":            isBasicType,
		"HasError":               hasError,
		"HasResult":              hasResult,
		"GenerateImports":        generateImports,
		"GenerateFlags":          generateFlags,
		"GenerateOptionsParsing": generateOptionsParsing,
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Funcs(funcMap).ParseFiles(templatePath)
	if err != nil {
		log.Errorf("Failed to parse template %s: %v", templatePath, err)
		return fmt.Errorf("failed to parse template %s: %v", templatePath, err)
	}

	// Debug log to inspect data
	log.Debugf("Data passed to template: %+v", data)

	f, err := os.Create(outputPath)
	if err != nil {
		log.Errorf("Failed to create file %s: %v", outputPath, err)
		return fmt.Errorf("failed to create file %s: %v", outputPath, err)
	}
	defer f.Close()

	err = tmpl.Execute(f, data)
	if err != nil {
		log.Errorf("Failed to execute template for file %s: %v", outputPath, err)
		return fmt.Errorf("failed to execute template: %v", err)
	}

	log.Debugf("Successfully generated file: %s", outputPath)
	return nil
}

// toCamelCase converts a snake_case string to CamelCase.
func toCamelCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		parts[i] = strings.Title(p)
	}
	return strings.Join(parts, "")
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

// toTitleCase converts a string to Title Case.
func toTitleCase(s string) string {
	return strings.Title(s)
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

// hasError checks if the command has an error return.
func hasError(data CommandData) bool {
	return data.HasError
}

// hasResult checks if the command has a result return.
func hasResult(data CommandData) bool {
	return data.HasResult
}

// generateImports generates the import statements based on the command data.
func generateImports(data CommandData) string {
	imports := []string{
		`"fmt"`,
		`"github.com/spf13/cobra"`,
	}
	if data.NeedsTFEImport {
		imports = append(imports, `"github.com/hashicorp/go-tfe"`)
	}
	if data.NeedsIOImport {
		imports = append(imports, `"io"`)
	}
	for _, imp := range data.AdditionalImports {
		imports = append(imports, imp)
	}
	return fmt.Sprintf("import (\n\t%s\n)", strings.Join(imports, "\n\t"))
}

// generateFlags generates the flag definitions based on the parameters.
func generateFlags(data CommandData) string {
	var flags []string
	for _, param := range data.Parameters {
		flagName := toSnakeCase(param.Name)
		switch param.Type {
		case "string", "tfe.StringOptions":
			flags = append(flags, fmt.Sprintf(`cmd.Flags().StringP("%s", "s", "", "Description for %s")`, flagName, param.Name))
		case "int", "tfe.IntOptions":
			flags = append(flags, fmt.Sprintf(`cmd.Flags().IntP("%s", "i", 0, "Description for %s")`, flagName, param.Name))
		case "bool", "tfe.BoolOptions":
			flags = append(flags, fmt.Sprintf(`cmd.Flags().BoolP("%s", "b", false, "Description for %s")`, flagName, param.Name))
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
		case "string", "tfe.StringOptions":
			parsing = append(parsing, fmt.Sprintf(`options.%s, _ = cmd.Flags().GetString("%s")`, field.Name, flagName))
		case "int", "tfe.IntOptions":
			parsing = append(parsing, fmt.Sprintf(`options.%s, _ = cmd.Flags().GetInt("%s")`, field.Name, flagName))
		case "bool", "tfe.BoolOptions":
			parsing = append(parsing, fmt.Sprintf(`options.%s, _ = cmd.Flags().GetBool("%s")`, field.Name, flagName))
		default:
			parsing = append(parsing, fmt.Sprintf(`// TODO: Parse flag for %s (%s)`, field.Name, field.Type))
		}
	}
	return strings.Join(parsing, "\n\t")
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
