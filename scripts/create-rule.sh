#!/bin/bash
#
# A comprehensive script to generate boilerplate rule YAML files.
# Supports: NATS/HTTP triggers & actions, simple & complex (nested) conditions,
# passthrough actions, custom headers, and forEach array processing.
#

set -e

# --- ANSI Color Codes for better output ---
COLOR_GREEN=$(tput setaf 2)
COLOR_YELLOW=$(tput setaf 3)
COLOR_BLUE=$(tput setaf 4)
COLOR_RESET=$(tput sgr0)

# --- Global variables ---
FILENAME=""
TRIGGER_BLOCK=""
CONDITIONS_BLOCK=""
ACTION_BLOCK=""
CONDITION_FIELDS_ARRAY=()

# --- Function Definitions ---

function show_help() {
  echo "Usage: ./create-rule.sh"
  echo "A script to interactively generate boilerplate rule YAML files."
  echo
  echo "The script will guide you through:"
  echo "  - Naming the rule file."
  echo "  - Choosing a Trigger (NATS or HTTP)."
  echo "  - Defining Conditions, including:"
  echo "    - Simple, Complex (nested), and Array operators (any/all/none)."
  echo "  - Choosing an Action (NATS or HTTP), including:"
  echo "    - Single actions (Templated or Passthrough)."
  echo "    - ForEach actions for batch processing with optional filters."
  echo
  echo "Options:"
  echo "  -h, --help    Display this help message and exit."
}

function print_context_help() {
    local indent="$1"
    echo -e "${indent}${COLOR_BLUE}--- Context Help ---"
    echo -e "${indent}Available Fields:"
    echo -e "${indent}  - Payload Fields: your.field, nested.object.field"
    echo -e "${indent}  - NATS Context:   @subject, @subject.0, @subject.count"
    echo -e "${indent}  - HTTP Context:   @path, @path.0, @method, @path.count"
    echo -e "${indent}  - Time Context:   @time.hour, @day.name, @date.iso, etc."
    echo -e "${indent}  - KV Context:     @kv.bucket.key:json.path"
    echo -e "${indent}  - Signature:      @signature.valid, @signature.pubkey"
    echo -e "${indent}Available Operators:"
    echo -e "${indent}  eq neq gt lt gte lte contains not_contains in not_in exists recent"
    echo -e "${indent}  any all none (for arrays in conditions)"
    echo -e "${indent}--------------------${COLOR_RESET}"
}

function validate_operator() {
    local op_to_check="$1"
    local operators=("eq" "neq" "gt" "lt" "gte" "lte" "contains" "not_contains" "in" "not_in" "exists" "recent" "any" "all" "none")
    for op in "${operators[@]}"; do
        if [[ "$op" == "$op_to_check" ]]; then return 0; fi
    done
    return 1
}

function get_filename() {
  while [ -z "$FILENAME" ]; do
    read -p "${COLOR_YELLOW}Enter the filename for the new rule (e.g., 'my_rule.yaml'): ${COLOR_RESET}" -r FILENAME
    if [ -z "$FILENAME" ]; then echo "Error: Filename cannot be empty." >&2; fi
  done
  
  if [[ ! "$FILENAME" == *.yaml && ! "$FILENAME" == *.yml ]]; then FILENAME="${FILENAME}.yaml"; fi
  if [ -f "$FILENAME" ]; then
    read -p "File '$FILENAME' already exists. Overwrite? (y/N): " -r OVERWRITE
    if [[ ! "$OVERWRITE" =~ ^[Yy]$ ]]; then echo "Cancelled."; exit 0; fi
  fi
}

function get_trigger() {
  echo; echo "${COLOR_BLUE}1. Select the Trigger Type (what starts the rule):${COLOR_RESET}"
  PS3="Your choice: "
  select TRIGGER_TYPE in "NATS (Message Bus)" "HTTP (Webhook)"; do
    case $TRIGGER_TYPE in
      "NATS (Message Bus)")
        read -p "${COLOR_YELLOW}   Enter NATS Trigger Subject (e.g., 'sensors.temp.>'): ${COLOR_RESET}" -r TRIGGER_SUBJECT
        TRIGGER_BLOCK=$(printf "  trigger:\n    nats:\n      subject: \"%s\"" "$TRIGGER_SUBJECT")
        break ;;
      "HTTP (Webhook)")
        read -p "${COLOR_YELLOW}   Enter HTTP Trigger Path (e.g., '/webhooks/github'): ${COLOR_RESET}" -r TRIGGER_PATH
        read -p "${COLOR_YELLOW}   Enter HTTP Method (e.g., 'POST', press Enter for all): ${COLOR_RESET}" -r TRIGGER_METHOD
        if [ -z "$TRIGGER_METHOD" ]; then
          TRIGGER_BLOCK=$(printf "  trigger:\n    http:\n      path: \"%s\"" "$TRIGGER_PATH")
        else
          TRIGGER_BLOCK=$(printf "  trigger:\n    http:\n      path: \"%s\"\n      method: \"%s\"" "$TRIGGER_PATH" "$(echo "$TRIGGER_METHOD" | tr '[:lower:]' '[:upper:]')")
        fi
        break ;;
      *) echo "Invalid option. Please try again." ;;
    esac
  done
}

## REFACTORED: Central function to get condition items, now with array operator support
function get_condition_items() {
    local indent="$1"
    local items_block=""
    while true; do
        read -p "${COLOR_YELLOW}${indent}  - Field (or press Enter to finish): ${COLOR_RESET}" -r C_FIELD
        [ -z "$C_FIELD" ] && break
        
        local C_OPERATOR=""; while [ -z "$C_OPERATOR" ]; do
            read -p "${COLOR_YELLOW}${indent}  - Operator: ${COLOR_RESET}" -r C_OPERATOR_INPUT
            if validate_operator "$C_OPERATOR_INPUT"; then C_OPERATOR="$C_OPERATOR_INPUT"; else echo "    Invalid operator."; fi
        done
        
        if [ -z "$items_block" ]; then items_block=$(printf "\n%sitems:" "$indent"); fi

        ## NEW: Interactive builder for array operators
        if [[ "$C_OPERATOR" == "any" || "$C_OPERATOR" == "all" || "$C_OPERATOR" == "none" ]]; then
            items_block+=$(printf "\n%s  - field: \"%s\"\n%s    operator: \"%s\"" "$indent" "$C_FIELD" "$indent" "$C_OPERATOR")
            echo -e "${COLOR_BLUE}${indent}    Now defining nested conditions for the '${C_OPERATOR}' operator...${COLOR_RESET}"
            local nested_conditions=$(get_conditions_recursive "${indent}    ")
            items_block+=$(printf "\n%s" "$nested_conditions")
        else
            read -p "${COLOR_YELLOW}${indent}  - Value: ${COLOR_RESET}" -r C_VALUE
            CONDITION_FIELDS_ARRAY+=("$C_FIELD")
            local quoted_value="$C_VALUE"; if ! [[ "$C_VALUE" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then quoted_value="\"$C_VALUE\""; fi
            items_block+=$(printf "\n%s  - field: \"%s\"\n%s    operator: \"%s\"\n%s    value: %s" "$indent" "$C_FIELD" "$indent" "$C_OPERATOR" "$indent" "$quoted_value")
        fi
    done
    echo -e "$items_block"
}

function get_conditions_recursive() {
    local indent="$1"
    local block=""
    read -p "${COLOR_YELLOW}${indent}Choose a logical operator for this group [and]: ${COLOR_RESET}" -r LOGICAL_OPERATOR
    LOGICAL_OPERATOR=${LOGICAL_OPERATOR:-and}
    block+=$(printf "%sconditions:\n%s  operator: \"%s\"" "$indent" "$indent" "$LOGICAL_OPERATOR")

    local items_block=$(get_condition_items "${indent}  ")
    [ -n "$items_block" ] && block+="$items_block"

    local groups_block=""; while true; do
        read -p "${COLOR_BLUE}${indent}Add another nested condition group here? (y/N): ${COLOR_RESET}" -r ADD_GROUP
        if [[ ! "$ADD_GROUP" =~ ^[Yy]$ ]]; then break; fi
        if [ -z "$groups_block" ]; then groups_block=$(printf "\n%sgroups:" "$indent"); fi
        groups_block+=$(printf "\n%s  -" "$indent")
        local nested_block=$(get_conditions_recursive "${indent}    ")
        groups_block+=$(printf "\n%s" "$nested_block")
    done
    [ -n "$groups_block" ] && block+="$groups_block"
    echo -e "$block"
}

function get_conditions() {
  echo; echo "${COLOR_BLUE}2. Define Conditions (when the rule should run):${COLOR_RESET}"
  print_context_help "    "
  PS3="Your choice: "
  ## NEW: Restored "Complex" option
  select CONDITION_TYPE in "No conditions" "Simple (a single list of checks)" "Complex (with nested groups)"; do
    case $CONDITION_TYPE in
      "No conditions") CONDITIONS_BLOCK="# No conditions defined for this rule."; break ;;
      "Simple (a single list of checks)")
        read -p "${COLOR_YELLOW}    Choose a logical operator for the group [and]: ${COLOR_RESET}" -r LOGICAL_OPERATOR
        LOGICAL_OPERATOR=${LOGICAL_OPERATOR:-and}
        local items_block=$(get_condition_items "    ")
        if [ -n "$items_block" ]; then
            CONDITIONS_BLOCK=$(printf "  conditions:\n    operator: \"%s\"%s" "$LOGICAL_OPERATOR" "$items_block")
        else
            CONDITIONS_BLOCK="# No conditions defined for this rule."
        fi
        break ;;
      "Complex (with nested groups)")
        local conditions_content=$(get_conditions_recursive "  ")
        CONDITIONS_BLOCK=$(printf "%s" "$conditions_content")
        break ;;
      *) echo "Invalid option." ;;
    esac
  done
}

function get_headers() {
    local header_block=""; read -p "Do you want to add custom headers to the action? (y/N): " -r ADD_HEADERS
    if [[ "$ADD_HEADERS" =~ ^[Yy]$ ]]; then
        header_block+="\n      headers:"; while true; do
            read -p "${COLOR_YELLOW}     - Header Name (or press Enter to finish): ${COLOR_RESET}" -r H_NAME
            [ -z "$H_NAME" ] && break
            read -p "${COLOR_YELLOW}     - Header Value: ${COLOR_RESET}" -r H_VALUE
            header_block+=$(printf "\n        %s: \"%s\"" "$H_NAME" "$H_VALUE")
        done
    fi; echo -e "$header_block"
}

function get_filter_conditions() {
    local indent="$1"
    local filter_block=""
    read -p "${COLOR_YELLOW}${indent}Do you want to add a filter to process only some elements? (y/N): ${COLOR_RESET}" -r ADD_FILTER
    if [[ "$ADD_FILTER" =~ ^[Yy]$ ]]; then
        read -p "${COLOR_YELLOW}${indent}  Choose a logical operator for the filter [and]: ${COLOR_RESET}" -r LOGICAL_OPERATOR
        LOGICAL_OPERATOR=${LOGICAL_OPERATOR:-and}
        filter_block=$(printf "\n    filter:\n%s  operator: \"%s\"" "$indent" "$LOGICAL_OPERATOR")
        
        local items_block=$(get_condition_items "${indent}  ")
        if [ -n "$items_block" ]; then
            filter_block+="$items_block"
        fi
    fi
    echo -e "$filter_block"
}

function get_action() {
  echo; echo "${COLOR_BLUE}3. Select the Action Type (what the rule does):${COLOR_RESET}"
  PS3="Your choice: "
  select ACTION_TYPE in "NATS (Publish Message)" "HTTP (Send Webhook)"; do
    case $ACTION_TYPE in
      "NATS (Publish Message)")
        echo "${COLOR_BLUE}   Select Action Cardinality:${COLOR_RESET}"; select CARDINALITY in "Single Action" "ForEach (Batch) Action"; do
            case $CARDINALITY in
                "Single Action")
                    read -p "${COLOR_YELLOW}   Enter NATS Action Subject (e.g., 'alerts.high_temp'): ${COLOR_RESET}" -r ACTION_SUBJECT
                    echo "${COLOR_BLUE}   Select Payload Type:${COLOR_RESET}"; select PAYLOAD_TYPE in "Templated (create a new JSON payload)" "Passthrough (forward original message)"; do
                        case $PAYLOAD_TYPE in
                            "Templated (create a new JSON payload)")
                                local details_block=""; for field in "${CONDITION_FIELDS_ARRAY[@]}"; do details_block+=$(printf "\n            \"%s\": \"{%s}\"," "$field" "$field"); done; details_block=${details_block%,}
                                local headers=$(get_headers)
                                ACTION_BLOCK=$(printf "  action:\n    nats:\n      subject: \"%s\"%s\n      payload: |\n        {\n          \"message\": \"Rule matched and processed.\",\n          \"details\": {%s\n          },\n          \"processed_at\": \"{@timestamp()}\"\n        }" "$ACTION_SUBJECT" "$headers" "$details_block")
                                break ;;
                            "Passthrough (forward original message)")
                                local headers=$(get_headers)
                                ACTION_BLOCK=$(printf "  action:\n    nats:\n      subject: \"%s\"%s\n      passthrough: true" "$ACTION_SUBJECT" "$headers")
                                break ;;
                            *) echo "Invalid option." ;;
                        esac
                    done; break ;;
                "ForEach (Batch) Action")
                    read -p "${COLOR_YELLOW}   Enter NATS Action Subject (can use element fields, e.g., 'alerts.{id}'): ${COLOR_RESET}" -r ACTION_SUBJECT
                    read -p "${COLOR_YELLOW}   Enter the path to the array field in the message (e.g., 'notifications'): ${COLOR_RESET}" -r FOREACH_FIELD
                    local filter_block=$(get_filter_conditions "    ")
                    local root_fields_block=""; for field in "${CONDITION_FIELDS_ARRAY[@]}"; do root_fields_block+=$(printf "\n          \"root_%s\": \"{@msg.%s}\"," "$(echo "$field" | sed 's/[^a-zA-Z0-9_]/_/g')" "$field"); done; root_fields_block=${root_fields_block%,}
                    ACTION_BLOCK=$(printf "  action:\n    forEach: \"%s\"%s\n    nats:\n      subject: \"%s\"\n      payload: |\n        {\n          # Fields from the array element\n          \"element_id\": \"{id}\",\n          \"element_status\": \"{status}\",\n\n          # Fields from the root message (using @msg prefix)\n          \"batch_id\": \"{@msg.batchId}\",%s\n\n          # System functions are always available\n          \"processed_at\": \"{@timestamp()}\"\n        }" "$FOREACH_FIELD" "$filter_block" "$ACTION_SUBJECT" "$root_fields_block")
                    break ;;
                *) echo "Invalid option." ;;
            esac
        done; break ;;
      "HTTP (Send Webhook)")
        echo "${COLOR_BLUE}   Select Action Cardinality:${COLOR_RESET}"; select CARDINALITY in "Single Action" "ForEach (Batch) Action"; do
            case $CARDINALITY in
                "Single Action")
                    read -p "${COLOR_YELLOW}   Enter HTTP Action URL (e.g., 'https://api.example.com/alerts'): ${COLOR_RESET}" -r ACTION_URL
                    read -p "${COLOR_YELLOW}   Enter HTTP Method [POST]: ${COLOR_RESET}" -r ACTION_METHOD; ACTION_METHOD=${ACTION_METHOD:-POST}
                    echo "${COLOR_BLUE}   Select Payload Type:${COLOR_RESET}"; select PAYLOAD_TYPE in "Templated (create a new JSON payload)" "Passthrough (forward original message)"; do
                        case $PAYLOAD_TYPE in
                            "Templated (create a new JSON payload)")
                                local details_block=""; for field in "${CONDITION_FIELDS_ARRAY[@]}"; do details_block+=$(printf "\n            \"%s\": \"{%s}\"," "$field" "$field"); done; details_block=${details_block%,}
                                local headers=$(get_headers)
                                ACTION_BLOCK=$(printf "  action:\n    http:\n      url: \"%s\"\n      method: \"%s\"%s\n      payload: |\n        {\n          \"alert\": \"Rule matched and processed.\",\n          \"details\": {%s\n          },\n          \"timestamp\": \"{@timestamp()}\"\n        }\n      retry:\n        maxAttempts: 3\n        initialDelay: \"1s\"" "$ACTION_URL" "$(echo "$ACTION_METHOD" | tr '[:lower:]' '[:upper:]')" "$headers" "$details_block")
                                break ;;
                            "Passthrough (forward original message)")
                                local headers=$(get_headers)
                                ACTION_BLOCK=$(printf "  action:\n    http:\n      url: \"%s\"\n      method: \"%s\"%s\n      passthrough: true\n      retry:\n        maxAttempts: 3\n        initialDelay: \"1s\"" "$ACTION_URL" "$(echo "$ACTION_METHOD" | tr '[:lower:]' '[:upper:]')" "$headers")
                                break ;;
                            *) echo "Invalid option." ;;
                        esac
                    done; break ;;
                "ForEach (Batch) Action")
                    read -p "${COLOR_YELLOW}   Enter HTTP Action URL (can use element fields, e.g., 'https://api.example.com/items/{id}'): ${COLOR_RESET}" -r ACTION_URL
                    read -p "${COLOR_YELLOW}   Enter HTTP Method [POST]: ${COLOR_RESET}" -r ACTION_METHOD; ACTION_METHOD=${ACTION_METHOD:-POST}
                    read -p "${COLOR_YELLOW}   Enter the path to the array field in the message (e.g., 'items'): ${COLOR_RESET}" -r FOREACH_FIELD
                    local filter_block=$(get_filter_conditions "    ")
                    local root_fields_block=""; for field in "${CONDITION_FIELDS_ARRAY[@]}"; do root_fields_block+=$(printf "\n          \"root_%s\": \"{@msg.%s}\"," "$(echo "$field" | sed 's/[^a-zA-Z0-9_]/_/g')" "$field"); done; root_fields_block=${root_fields_block%,}
                    ACTION_BLOCK=$(printf "  action:\n    forEach: \"%s\"%s\n    http:\n      url: \"%s\"\n      method: \"%s\"\n      payload: |\n        {\n          # Fields from the array element\n          \"id\": \"{id}\",\n          \"status\": \"{status}\",\n\n          # Fields from the root message (using @msg prefix)\n          \"batch_id\": \"{@msg.batchId}\",%s\n\n          # System functions are always available\n          \"processed_at\": \"{@timestamp()}\"\n        }\n      retry:\n        maxAttempts: 3\n        initialDelay: \"1s\"" "$FOREACH_FIELD" "$filter_block" "$ACTION_URL" "$(echo "$ACTION_METHOD" | tr '[:lower:]' '[:upper:]')" "$root_fields_block")
                    break ;;
                *) echo "Invalid option." ;;
            esac
        done; break ;;
      *) echo "Invalid option. Please try again." ;;
    esac
  done
}

# --- Main Script Logic ---

if [[ "$1" == "-h" || "$1" == "--help" ]]; then show_help; exit 0; fi

echo "${COLOR_GREEN}--- Universal Rule Boilerplate Generator ---${COLOR_RESET}"
echo "This script will interactively build a rule file for you."

get_filename
get_trigger
get_conditions
get_action

# --- Generate the YAML File ---
cat << EOF > "$FILENAME"
# Rule file created on $(date)
#
# This is a boilerplate rule generated by the create-rule.sh script.
# You can further customize this file in your editor.
#
-
${TRIGGER_BLOCK}

${CONDITIONS_BLOCK}

${ACTION_BLOCK}
EOF

# --- Final Confirmation ---
echo; echo "${COLOR_GREEN}✔ Success! Rule file '${FILENAME}' created with the following structure:${COLOR_RESET}"
echo "--------------------------------------------------"
cat "$FILENAME"
echo "--------------------------------------------------"
echo; echo "You can now edit '${FILENAME}' to further customize the payload and logic."
