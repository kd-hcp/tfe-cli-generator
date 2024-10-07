# Terraform Enterprise CLI Generator

## Overview

This project is a code generator designed to create a Command Line Interface (CLI) for interacting with HashiCorp's Terraform Enterprise (TFE) API. The generator parses the Terraform Enterprise Go library (`go-tfe`) to generate command definitions for services, methods, and options. The generated CLI allows users to run commands directly from the terminal to interact with their Terraform Enterprise instance.

The repository is structured into a generator (`generator/generator.go`) responsible for generating Go command files using templates located in the `templates/` directory.

### Generated CLI

The generated CLI consists of:
- `main.go`: The entry point for the CLI application.
- `root.go`: Defines the root command for the CLI.
- Command files for each Terraform Enterprise service and method.

This CLI provides commands that allow users to create, list, read, update, and delete resources like variables, workspaces, stacks, and more, through the TFE API.

## Project Structure

```
tfe-cli/
|-- generator/
|   |-- generator.go
|-- templates/
|   |-- command.go.tmpl
|   |-- main.go.tmpl
|   |-- root.go.tmpl
```

- **`generator/generator.go`**: The primary generator script that scans the `go-tfe` library and generates CLI code for each service and method found.
- **`templates/`**: Contains the Go template files for generating CLI code.
  - **`command.go.tmpl`**: Template for generating each command file.
  - **`main.go.tmpl`**: Template for the main entry point of the CLI (`main.go`).
  - **`root.go.tmpl`**: Template for the root command (`root.go`).

## Requirements

- **Go** (1.17 or higher)
- **`go-tfe` library**: The Go library for interacting with Terraform Enterprise.
- **Cobra**: A library for creating powerful modern CLI applications.

### Installation

1. **Clone the Repository**:
   ```sh
   git clone <repository_url>
   cd tfe-cli
   ```

2. **Install Dependencies**:
   Make sure to install the required dependencies for Go and Cobra:
   ```sh
   go get github.com/spf13/cobra
   go get github.com/hashicorp/go-tfe
   ```

3. **Run the Generator**:
   ```sh
   go run generator/generator.go <path_to_go_tfe_library>
   ```
   This will generate the CLI in the `tfe-cli/cmd/` directory.

4. **Build the CLI**:
   ```sh
   go build -o tfe-cli main.go
   ```

## Usage

### Running the CLI

After building, run the CLI by executing the compiled binary:
```sh
./tfe-cli
```

You can get help by using:
```sh
./tfe-cli -h
```

### Example Commands

Below are some examples of commands that are supported by the generated CLI:

- **List Workspaces**:
  ```sh
  ./tfe-cli workspaces list --organization=my-org
  ```

- **Create a Variable**:
  ```sh
  ./tfe-cli variables create --workspace-id=ws-1234 --key=my_var --value=my_value
  ```

- **Read Stack Information**:
  ```sh
  ./tfe-cli stacks read --stack-id=st-5678
  ```

## Extending the CLI

The CLI is generated dynamically from the `go-tfe` library, and you can extend it by adding support for new services and methods by editing the `command.go.tmpl` file and running the generator again.

### How It Works

1. **Scan the Library**: The `generator/generator.go` script scans the Terraform Enterprise Go library to collect information on the available services and methods.

2. **Generate Command Files**: For each method, the `command.go.tmpl` template is used to generate a command that can be invoked via the CLI.

3. **Add Commands to Root**: The generated commands are added to the root command (`root.go`), making them accessible to the CLI.

### Template Functions

Custom template functions are provided to the templates through a `FuncMap` to make the generated code dynamic:

- **`generateImports(data CommandData)`**: Generates the import statements required for each command file.
- **`getDefaultValueForType(type string)`**: Provides default values based on the type for initializing fields.
- **`toSnakeCase(s string)`**: Converts CamelCase to snake_case.
- **`stripSlicePrefix(type string)`**: Strips `[]` from the beginning of a type.
- **`strings`**: Helper functions like `hasPrefix`, `split`, etc., are exposed to templates.

## Troubleshooting

### Common Issues

1. **Duplicate Import Statements**: The generator has been updated to ensure imports are generated without duplicates. If duplicates occur, double-check the `generateImports` function and ensure that imports are correctly populated.

2. **Undefined Flags**: When parsing flags, ensure that the parameter types are supported by the `cmd.Flags()` functions provided by Cobra. If a type isn't supported, add a custom handler to convert or handle the type as necessary.

3. **Missing Imports for Required Libraries**: Imports like `fmt`, `os`, `context`, and `io` are dynamically added based on the generated code's requirements. If an import is missing, ensure that the `generateImports` function is properly handling the cases where these libraries are needed.

## Contributing

Contributions are welcome! Please follow the steps below to contribute:

1. **Fork the Repository**.
2. **Create a New Branch**.
   ```sh
   git checkout -b feature/new-feature
   ```
3. **Make Your Changes**.
4. **Test Your Changes**.
5. **Submit a Pull Request**.

Please make sure to document your changes and update the README where necessary.

## License

This project is licensed under the MIT License. See the LICENSE file for details.

## Contact

If you have questions or run into issues, please feel free to open an issue or reach out via the contact information provided in the repository.

