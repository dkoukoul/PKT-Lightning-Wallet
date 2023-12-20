////////////////////////////////////////////////////////////////////////////////
//	lndcli/lnd_help.go  -  Apr-12-2022  -  aldebap
//
//	Invoke the pld REST help and show it in a fancy way for the CLI
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/generated/proto/restrpc_pb/help_pb"
	"github.com/pkt-cash/pktd/pktlog/log"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func bright(str string) string {
	return log.Bright + str + log.Reset
}

//	show a fancy output for the master help
func getMasterHelp(pldServer string) er.R {

	response, errr := http.Get(pldServer + "/api/v1/help")
	if errr != nil {
		return er.New("Fail executing pld command: " + errr.Error())
	}

	responsePayload, err := io.ReadAll(response.Body)
	if err != nil {
		return er.Errorf("Fail reading command response payload from pld server: %s", err)
	}
	if err := checkForServerError(responsePayload); err != nil {
		return er.Errorf("Unable to get help master help from the server, is [%s] a pld instance?\nError: [%s]",
			pldServer, err.Message())
	}

	mainCat := help_pb.Category{}
	if err := protojson.Unmarshal(responsePayload, &mainCat); err != nil {
		return er.Errorf("Failed to unmarshal help response payload [%s]", err)
	}

	//var masterHelp = help.RESTMaster_help()

	fmt.Fprintf(os.Stdout, "NAME\n    pld - Lightning Network Daemon REST interface (pld)\n\n")

	fmt.Fprintf(os.Stdout, "DESCRIPTION\n")
	for _, line := range []string{
		"These are the commands which can be invoked with pldctl",
		"or using the API directly. Each command has help information",
		"which can be accessed by typing " + bright("pldctl help <the command>") + ".",
		"For example: " + bright("pldctl help wallet/unlock"),
		"",
		"For a safer way to unlock your wallet without it being displayed",
		"on the terminal you can use the "+bright("pldctl unlock"),
		"or " + bright("pldctl unlock --timeout=<seconds>") + "to set a timeout,"+
		"or " + bright("pldctl unlock --start_lightning")+" to start lightning",
		"which will prompt you for your wallet password.",
		"",
		"In addition, each command corresponds to an RPC endpoint which",
		"can be requested directly. So for example the command:",
		bright("pldctl meta/getinfo") + " is the same as API request:",
		bright(fmt.Sprintf("curl %s/api/v1/meta/getinfo", pldServer)),
		"",
		"Commands which require input arguments are done as POST messages",
		bright("pldctl wallet/unlock --wallet_passphrase=password"),
		"is the same as the API request:",
		bright("curl -X POST -H Content-Type:application/json -d \\"),
		bright(fmt.Sprintf("    '{\"wallet_passphrase\":\"password\"}' %s/api/v1/wallet/unlock", pldServer)),
		"",
		"You can also see individualized help on any endpoint using a command like",
		bright("pldctl help wallet/unlock") + " which is the same as the API request:",
		bright(fmt.Sprintf("curl %s/api/v1/help/wallet/unlock", pldServer)),
		"",
		"And finally, this help can be shown as an API endpoint:",
		bright(fmt.Sprintf("curl %s/api/v1/help", pldServer)),
		"For additional options when using pldctl, use " + bright("pldctl --help"),
	} {
		fmt.Fprintf(os.Stdout, "%s\n", line)
	}
	fmt.Fprintf(os.Stdout, "\n")

	fmt.Fprintf(os.Stdout, "CATEGORIES\n")
	for name, category := range mainCat.Categories {
		showCategory(name, category, "")
	}

	return nil
}

//	show a fancy output for the help on a specific category
func showCategory(name string, category *help_pb.Category, indent string) {

	indent1 := indent + " |"

	fmt.Fprintf(os.Stdout, "%s %s\n%s\n", indent, bright(name), indent1)

	for _, line := range category.Description {
		fmt.Fprintf(os.Stdout, "%s %s\n", indent1, line)
	}
	fmt.Fprintf(os.Stdout, "%s\n", indent1)

	if len(category.Endpoints) > 0 {
		fmt.Fprintf(os.Stdout, "%s COMMANDS\n", indent1)
		for _, endpoint := range category.Endpoints {
			command := strings.TrimPrefix(endpoint.HelpPath, "/api/v1/help/")
			fmt.Fprintf(os.Stdout, "%s %s : %s\n", indent1, bright(command), endpoint.Brief)
		}
	}

	if len(category.Categories) > 0 {
		fmt.Fprintf(os.Stdout, "%s\n", indent1)
		fmt.Fprintf(os.Stdout, "%s CATEGORIES\n", indent1)
		for name, subcategory := range category.Categories {
			showCategory(name, subcategory, indent1)
		}
	} else {
		fmt.Fprintf(os.Stdout, "%s\n", indent)
	}
}

func getEndpointHelp(helpUrl string) (*help_pb.EndpointHelp, er.R) {
	out := help_pb.EndpointHelp{}
	req, err := http.NewRequest("GET", helpUrl, nil)
	if err != nil {
		return nil, er.E(err)
	}
	req.Header.Add("Content-Type", "application/protobuf")
	if res, err := http.DefaultClient.Do(req); err != nil {
		return nil, er.E(err)
	} else if res.StatusCode != 200 {
		if res.StatusCode == 404 {
			return nil, er.Errorf("No such endpoint, try `pldctl help` for a list")
		}
		return nil, er.Errorf("Unexpected status code: [%d] from url [%s]",
			res.StatusCode, helpUrl)
	} else if b, err := ioutil.ReadAll(res.Body); err != nil {
		return nil, er.E(err)
	} else if err := proto.Unmarshal(b, &out); err != nil {
		return nil, er.E(err)
	}
	return &out, nil
}

//	show a fancy output for the help on a specific command
func printCommandHelp(endpointHelp *help_pb.EndpointHelp) er.R {
	for _, line := range endpointHelp.Description {
		fmt.Fprintf(os.Stdout, "  %s\n", line)
	}
	fmt.Fprintf(os.Stdout, "\n")

	if len(endpointHelp.Request.Fields) > 0 {
		fmt.Fprintf(os.Stdout, "OPTIONS:\n")
		for _, requestField := range endpointHelp.Request.Fields {
			showField("", requestField)
		}
	}

	return nil
}

//	show help line on a specific command CLI argument
func showField(fieldHierarchy string, requestField *help_pb.Field) {

	if len(requestField.Type.Fields) == 0 {

		var commandOption string

		if len(fieldHierarchy) == 0 {
			commandOption = "--" + requestField.Name
		} else {
			commandOption = "--" + fieldHierarchy + "." + requestField.Name
		}

		switch requestField.Type.Name {
		case "bool":

		case "[]byte":
			commandOption += "=value"
		case "string":
			commandOption += "=value"
		case "uint32":
			commandOption += "=value"
		case "int32":
			commandOption += "=value"
		case "uint64":
			commandOption += "=value"
		case "int64":
			commandOption += "=value"
		}

		if len(requestField.Description) == 0 {
			fmt.Fprintf(os.Stdout, "  %s\n", commandOption)
		} else {
			for i, description := range requestField.Description {
				if i == 0 {
					fmt.Fprintf(os.Stdout, "  %s - %s\n", commandOption, description)
				} else {
					fmt.Fprintf(os.Stdout, "    %s\n", description)
				}
			}
		}
	} else {

		for _, requestSubField := range requestField.Type.Fields {
			if len(fieldHierarchy) == 0 {
				showField(requestField.Name, requestSubField)
			} else {
				showField(fieldHierarchy+"."+requestField.Name, requestSubField)
			}
		}
	}
}
