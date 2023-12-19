////////////////////////////////////////////////////////////////////////////////
//	lndcli/main.go  -  Apr-8-2022  -  aldebap
//
//	Entry point for the pld client using the REST APIs
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/generated/proto/restrpc_pb/help_pb"
	"golang.org/x/crypto/ssh/terminal"
)

func main() {
	if err := main1(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %s", err)
		os.Exit(100)
	}
}

func main1() er.R {
	var showRequestPayload bool
	pldServer := "http://localhost:8080"

	//	parse command line arguments
	flag.StringVar(&pldServer, "pld_server", "http://localhost:8080", "set the pld server URL")
	flag.BoolVar(&showRequestPayload, "show_req_payload", false, "show the request payload before invoke the pld command")

	flag.Parse()

	//	if a protocol is missing from pld_server, assume HTTP as default
	if !strings.HasPrefix(pldServer, "http://") && !strings.HasPrefix(pldServer, "https://") {
		pldServer = "http://" + pldServer
	}

	if len(flag.Args()) == 0 {
		return getMasterHelp(pldServer)
	}

	//	one or more arguments means the help + command
	//		or command to be executed followed by arguments to build request payload
	command := flag.Args()[0]
		isHelp := false

	//	if the user wants help on a command
	if command == "help" {
		if len(flag.Args()) == 1 {
			return getMasterHelp(pldServer)
		}
		isHelp = true
		command = flag.Args()[1]
	}
	if command == "unlock" {
		startLightning := false
		timeout := 300
		if len(flag.Args()) >= 2 {
			// Check if the next argument is --timeout
			argument := flag.Args()[1]
			if strings.Contains(argument, "--timeout") {
				splitArg := strings.Split(argument, "=")
				if len(splitArg) > 1 {
					var err error
					timeout, err = strconv.Atoi(splitArg[1])
					if err != nil {
						fmt.Println("Error converting timeout value to int, setting to 300:", err)
					} else {
						fmt.Printf("Unlocking for %d seconds...\n", timeout)
					}
				}
			} else if strings.Contains(argument, "--start_lightning") {
				startLightning = true
			}
		}

		fmt.Print("Enter password for wallet: ")
		password, err := terminal.ReadPassword(int(os.Stdin.Fd()))
		if err != nil {
			fmt.Println("Error:", err)
			return er.Errorf("error: unable to read password\n")
		}
		fmt.Println("")
		if !startLightning {
			requestPayload := "{ \"wallet_passphrase\": \"" + string(password) + "\", \"timeout_seconds\":" + strconv.Itoa(timeout) + " }"
			return executeCommand(pldServer, "wallet/unlock", requestPayload)
		} else {
			requestPayload := "{ \"wallet_passphrase\": \"" + string(password) + "\" }"
			return executeCommand(pldServer, "lightning/start", requestPayload)
		}
	}
	help, err := getEndpointHelp(pldServer + "/api/v1/help/" + command)
	if err != nil {
		return err
	}
	if isHelp {
		if len(flag.Args()) == 2 {
			return printCommandHelp(help)
		} else {
			return er.Errorf("error: unexpected arguments for help on command %v\n", flag.Args()[2:])
		}
	}

	//	first argument is a pld command followed by arguments to build request payload
	requestPayload, err := formatRequestPayload(help, flag.Args()[1:])
	if err != nil {
		return err
	}
	//	if necessary, indent the request payload before show it
	if showRequestPayload {
		var requestPayloadMap map[string]interface{}

		err = er.E(json.Unmarshal([]byte(requestPayload), &requestPayloadMap))
		if err != nil {
		} else {
			prettyRequestPayload, err := er.E1(json.MarshalIndent(requestPayloadMap, "", "    "))
			if err != nil {
			} else {
				fmt.Fprintf(os.Stdout, "[trace]: request payload: %s\n", string(prettyRequestPayload))
			}
		}
	}

	//	send the request payload to pld
	return executeCommand(pldServer, command, requestPayload)
}

//	based on pld's command path, parse the CLI arguments to build the request payload
func formatRequestPayload(endpointHelp *help_pb.EndpointHelp, arguments []string) (string, er.R) {

	//	build request payload based on request's help info hierarchy
	var parsedArgument []bool = make([]bool, len(arguments))
	var requestPayload string

	if endpointHelp.Request != nil {
		for _, requestField := range endpointHelp.Request.Fields {

			formattedField, err := formatRequestField("", requestField, arguments, &parsedArgument)
			if err != nil {
				return "", er.New("error parsing arguments: " + err.Error())
			}

			if len(formattedField) > 0 {
				if len(requestPayload) > 0 {
					requestPayload += ", "
				}
				requestPayload += formattedField
			}
		}
	}
	if len(requestPayload) == 0 && util.Contains(endpointHelp.Features, help_pb.F_ALLOW_GET) {
		requestPayload = ""
	} else {
		requestPayload = "{ " + requestPayload + " }"
	}

	//	check if there are invalid arguments (not parsed)
	if len(arguments) > 0 {
		for i := 0; i < len(parsedArgument); i++ {
			if !parsedArgument[i] {
				return "", er.New("invalid command argument: " + arguments[i])
			}
		}
	}

	return requestPayload, nil
}

//	check if there's a CLI argument for a specific payload field,
//	in which case, returs the field formatted accordingly to it's data type
func formatRequestField(fieldHierarchy string, requestField *help_pb.Field, arguments []string, parsedArgument *[]bool) (string, error) {

	var formattedField string

	if len(requestField.Type.Fields) == 0 {

		var commandOption string

		if len(fieldHierarchy) == 0 {
			commandOption = "--" + requestField.Name
		} else {
			commandOption = "--" + fieldHierarchy + "." + requestField.Name
		}

		switch requestField.Type.Name {
		case "bool":
			for i, argument := range arguments {
				if argument == commandOption {
					formattedField += "\"" + requestField.Name + "\": true"
					(*parsedArgument)[i] = true
				}
			}

		case "[]byte":
			commandOption += "="
			for i, argument := range arguments {
				if strings.HasPrefix(argument, commandOption) {
					formattedField += "\"" + requestField.Name + "\": \"" + argument[len(commandOption):] + "\""
					(*parsedArgument)[i] = true
				}
			}

		case "string":
			commandOption += "="
			for i, argument := range arguments {
				if strings.HasPrefix(argument, commandOption) {
					if !requestField.Repeated {
						formattedField += "\"" + requestField.Name + "\": \"" + argument[len(commandOption):] + "\""
					} else {
						//	make sure the array is delimited by square brackets
						arrayArgument := strings.TrimSpace(argument[len(commandOption):])

						if arrayArgument[0] != '[' || arrayArgument[len(arrayArgument)-1] != ']' {
							return "", errors.New("array argument must be delimitted by square brackets: " + arrayArgument)
						}

						//	each string in the array is comma separated
						var arrayOfStrings string

						for _, stringValue := range strings.Split(arrayArgument[1:len(arrayArgument)-1], ",") {
							stringValue = strings.TrimSpace(stringValue)

							//	make sure the string element is delimited by double quotes
							if stringValue[0] != '"' || stringValue[len(stringValue)-1] != '"' {
								return "", errors.New("array element must be delimitted by double quotes: " + stringValue)
							}

							if len(arrayOfStrings) > 0 {
								arrayOfStrings += ", "
							}
							arrayOfStrings += stringValue
						}

						formattedField += "\"" + requestField.Name + "\": [ " + arrayOfStrings + " ]"
					}
					(*parsedArgument)[i] = true
				}
			}

		//	TODO: to make sure that for integer types pld doen't have arrays (Repeated == true)
		case "uint32":
			commandOption += "="
			for i, argument := range arguments {
				if strings.HasPrefix(argument, commandOption) {
					formattedField += "\"" + requestField.Name + "\": " + argument[len(commandOption):]
					(*parsedArgument)[i] = true
				}
			}

		case "int32":
			commandOption += "="
			for i, argument := range arguments {
				if strings.HasPrefix(argument, commandOption) {
					formattedField += "\"" + requestField.Name + "\": " + argument[len(commandOption):]
					(*parsedArgument)[i] = true
				}
			}

		case "uint64":
			commandOption += "="
			for i, argument := range arguments {
				if strings.HasPrefix(argument, commandOption) {
					formattedField += "\"" + requestField.Name + "\": " + argument[len(commandOption):]
					(*parsedArgument)[i] = true
				}
			}

		case "int64":
			commandOption += "="
			for i, argument := range arguments {
				if strings.HasPrefix(argument, commandOption) {
					formattedField += "\"" + requestField.Name + "\": " + argument[len(commandOption):]
					(*parsedArgument)[i] = true
				}
			}

		case "float64":
			commandOption += "="
			for i, argument := range arguments {
				if strings.HasPrefix(argument, commandOption) {
					formattedField += "\"" + requestField.Name + "\": " + argument[len(commandOption):]
					(*parsedArgument)[i] = true
				}
			}

		//	enums are formatted as it's name, because pld is able to unmarshall them
		case "ENUM_VARIENT":
			for i, argument := range arguments {
				if argument == commandOption {
					formattedField += "\"" + requestField.Name + "\""
					(*parsedArgument)[i] = true
				}
			}
		}
	} else {

		//	field composed of sub-fields
		var formattedSubFields string

		for _, requestSubField := range requestField.Type.Fields {
			var formattedSubField string
			var err error

			if len(fieldHierarchy) == 0 {
				formattedSubField, err = formatRequestField(requestField.Name, requestSubField, arguments, parsedArgument)
			} else {
				formattedSubField, err = formatRequestField(fieldHierarchy+"."+requestField.Name, requestSubField, arguments, parsedArgument)
			}
			if err != nil {
				return "", err
			}

			if len(formattedSubField) > 0 {
				if len(formattedSubFields) > 0 {
					formattedSubFields += ", "
				}
				formattedSubFields += formattedSubField
			}
		}
		if len(formattedSubFields) > 0 {
			if !requestField.Repeated {
				formattedField += "\"" + requestField.Name + "\": { " + formattedSubFields + " }"
			} else {
				formattedField += "\"" + requestField.Name + "\": [ " + formattedSubFields + " ]"
			}
		}
	}

	return formattedField, nil
}

//	invoke pld's REST endpoint and try to parse error messages eventually returned by the server
func executeCommand(pldServer string, command string, payload string) er.R {

	var response *http.Response
	var errr error

	commandURI := pldServer + "/api/v1/" + command

	//	if there's no payload, use HTTP GET method to invoke pld command, otherwise use POST method
	if len(payload) == 0 {
		response, errr = http.Get(commandURI)
		if errr != nil {
			return er.New("fail executing pld command: " + errr.Error())
		}
	} else {
		response, errr = http.Post(commandURI, "application/json", strings.NewReader(payload))
		if errr != nil {
			return er.New("fail executing pld command: " + errr.Error())
		}
	}
	defer response.Body.Close()

	responsePayload, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fail reading command response payload from pld server: %s", err)
		panic(-1)
	}
	if err := checkForServerError(responsePayload); err != nil {
		return er.New(err.Message() + "\nTry \"pldctl help " + command + "\" for more informaton on this command")
	}

	fmt.Fprintf(os.Stdout, "%s\n", responsePayload)

	return nil
}

type pldErrorResponse struct {
	Message string   `json:"message,omitempty"`
	Stack   []string `json:"stack,omitempty"`
}

//	parse a response payload to check if it indicates an error messages returned by the server
func checkForServerError(responsePayload []byte) er.R {
	var errorResponse pldErrorResponse

	err := json.Unmarshal(responsePayload, &errorResponse)
	if err == nil {
		if len(errorResponse.Message) > 0 && len(errorResponse.Stack) > 0 {
			var stackTrace string

			//	format the stack trance for output
			for _, step := range errorResponse.Stack {
				stackTrace += step + "\n"
			}

			return er.New("pld returned an error message: " + errorResponse.Message + "\n\npld stack trace: " + stackTrace)
		}
	}

	return nil
}
