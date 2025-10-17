#!/bin/bash
#
# A comprehensive script to generate boilerplate rule YAML files.
# Supports: NATS/HTTP triggers & actions, simple & complex (nested) conditions,
# passthrough actions, and custom headers.
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
  echo "  - Defining Conditions (Simple or Complex/Nested)."
  echo "  - Choosing an Action (NATS or HTTP)."
  echo
  echo "Options:"
  echo "  -h, --help    Display this help message and exit."
}

# This function now ONLY prints to the console. It is never captured in a variable.
function print_context_help() {
    local indent="$1"
    echo -e "${indent}${COLOR_BLUE}--- Condition Help ---"
    echo -e "${indent}Available Fields:"
    echo -e "${indent}  - Payload Fields: your.field, nested.object.field"
    echo -e "${indent}  - NATS Context:   @subject, @subject.0, @subject.count"
    echo -e "${indent}  - HTTP Context:   @path, @path.0, @method, @path.count"
    echo -e "${indent}  - Time Context:   @time.hour, @day.name, @date.iso, etc."
    echo -e "${indent}  - KV Context:     @kv.bucket.key:json.path"
    echo -e "${indent}  - Signature:      @signature.valid, @signature.pubkey"
    echo -e "${indent}Available Operators:"
    echo -e "${indent}  eq neq gt lt gte lte contains not_contains in not_in exists recent"
    echo -e "${indent}--------------------${COLOR_RESET}"
}

function validate_operator() {
    local op_to_check="$1"
    local operators=("eq" "neq" "gt" "lt" "gte" "lte" "contains" "not_contains" "in" "not_in" "exists" "recent")
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

function get_simple_conditions() {
    local indent="    "
    local block=""
    read -p "${COLOR_YELLOW}${indent}Choose a logical operator for the group [and]: ${COLOR_RESET}" -r LOGICAL_OPERATOR
    LOGICAL_OPERATOR=${LOGICAL_OPERATOR:-and}
    block=$(printf "  conditions:\n%soperator: \"%s\"\n%sitems:" "$indent" "$LOGICAL_OPERATOR" "$indent")

    while true; do
        read -p "${COLOR_YELLOW}${indent}  - Field (or press Enter to finish): ${COLOR_RESET}" -r C_FIELD
        [ -z "$C_FIELD" ] && break
        local C_OPERATOR=""; while [ -z "$C_OPERATOR" ]; do
            read -p "${COLOR_YELLOW}${indent}  - Operator: ${COLOR_RESET}" -r C_OPERATOR_INPUT
            if validate_operator "$C_OPERATOR_INPUT"; then C_OPERATOR="$C_OPERATOR_INPUT"; else echo "    Invalid operator."; fi
        done
        read -p "${COLOR_YELLOW}${indent}  - Value: ${COLOR_RESET}" -r C_VALUE
        CONDITION_FIELDS_ARRAY+=("$C_FIELD")
        local quoted_value="$C_VALUE"; if ! [[ "$C_VALUE" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then quoted_value="\"$C_VALUE\""; fi
        block+=$(printf "\n%s  - field: \"%s\"\n%s    operator: \"%s\"\n%s    value: %s" "$indent" "$C_FIELD" "$indent" "$C_OPERATOR" "$indent" "$quoted_value")
    done
    echo -e "$block"
}

function get_conditions_recursive() {
    local indent="$1"
    local block=""
    read -p "${COLOR_YELLOW}${indent}Choose a logical operator for this group [and]: ${COLOR_RESET}" -r LOGICAL_OPERATOR
    LOGICAL_OPERATOR=${LOGICAL_OPERATOR:-and}
    block+=$(printf "%soperator: \"%s\"" "$indent" "$LOGICAL_OPERATOR")

    local items_block="";
    while true; do
        read -p "${COLOR_YELLOW}${indent}  - Field (or press Enter to finish): ${COLOR_RESET}" -r C_FIELD
        [ -z "$C_FIELD" ] && break
        local C_OPERATOR=""; while [ -z "$C_OPERATOR" ]; do
            read -p "${COLOR_YELLOW}${indent}  - Operator: ${COLOR_RESET}" -r C_OPERATOR_INPUT
            if validate_operator "$C_OPERATOR_INPUT"; then C_OPERATOR="$C_OPERATOR_INPUT"; else echo "    Invalid operator."; fi
        done
        read -p "${COLOR_YELLOW}${indent}  - Value: ${COLOR_RESET}" -r C_VALUE
        CONDITION_FIELDS_ARRAY+=("$C_FIELD")
        local quoted_value="$C_VALUE"; if ! [[ "$C_VALUE" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then quoted_value="\"$C_VALUE\""; fi
        if [ -z "$items_block" ]; then items_block=$(printf "\n%sitems:" "$indent"); fi
        items_block+=$(printf "\n%s  - field: \"%s\"\n%s    operator: \"%s\"\n%s    value: %s" "$indent" "$C_FIELD" "$indent" "$C_OPERATOR" "$indent" "$quoted_value")
    done
    [ -n "$items_block" ] && block+="$items_block"

    local groups_block=""; while true; do
        read -p "${COLOR_BLUE}${indent}Add a nested condition group here? (y/N): ${COLOR_RESET}" -r ADD_GROUP
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
  echo; echo "${COLOR_BLUE}2. Select a Condition Type:${COLOR_RESET}"
  PS3="Your choice: "
  select CONDITION_TYPE in "No conditions" "Simple (a single list of checks)" "Complex (with nested groups)"; do
    case $CONDITION_TYPE in
      "No conditions") CONDITIONS_BLOCK="# No conditions defined for this rule."; break ;;
      "Simple (a single list of checks)")
        print_context_help "    " # **FIX**: Print help to console BEFORE capturing output
        CONDITIONS_BLOCK=$(get_simple_conditions)
        break ;;
      "Complex (with nested groups)")
        print_context_help "    " # **FIX**: Print help to console BEFORE capturing output
        local conditions_content=$(get_conditions_recursive "    ")
        CONDITIONS_BLOCK=$(printf "  conditions:\n%s" "$conditions_content")
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

function get_action() {
  echo; echo "${COLOR_BLUE}3. Select the Action Type (what the rule does):${COLOR_RESET}"
  PS3="Your choice: "
  select ACTION_TYPE in "NATS (Publish Message)" "HTTP (Send Webhook)"; do
    case $ACTION_TYPE in
      "NATS (Publish Message)")
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
      "HTTP (Send Webhook)")
        read -p "${COLOR_YELLOW}   Enter HTTP Action URL (e.g., 'https://api.example.com/alerts'): ${COLOR_RESET}" -r ACTION_URL
        read -p "${COLOR_YELLOW}   Enter HTTP Method [POST]: ${COLOR_RESET}" -r ACTION_METHOD; ACTION_METHOD=${ACTION_METHOD:-POST}
        echo "${COLOR_BLUE}   Select Payload Type:${COLOR_RESET}"; select PAYLOAD_TYPE in "Templated (create a new JSON payload)" "Passthrough (forward original message)"; do
            case $PAYLOAD_TYPE in
                "Templated (create a new JSON payload)")
                    local details_block=""; for field in "${CONDITION_FIELDS_ARRAY[@]}"; do details_block+=$(printf "\n            \"%s\": \"{%s}\"," "$field" "$field"); done; details_block=${details_block%,}
                    local headers=$(get_headers)
                    ACTION_BLOCK=$(printf "  action:\n    http:\n      url: \"%s\"\n      method: \"%s\"%s\n      payload: |\n        {\n          \"alert\": \"Rule matched and processed.\",\n          \"details\": {%s\n          },\n          \"timestamp\": \"{@timestamp()}\"\n        }\n      retry:\n        maxAttempts: 3      # Default: 3\n        initialDelay: \"1s\"  # Default: 1s\n        maxDelay: \"30s\"     # Default: 30s" "$ACTION_URL" "$(echo "$ACTION_METHOD" | tr '[:lower:]' '[:upper:]')" "$headers" "$details_block")
                    break ;;
                "Passthrough (forward original message)")
                    local headers=$(get_headers)
                    ACTION_BLOCK=$(printf "  action:\n    http:\n      url: \"%s\"\n      method: \"%s\"%s\n      passthrough: true\n      retry:\n        maxAttempts: 3      # Default: 3\n        initialDelay: \"1s\"  # Default: 1s\n        maxDelay: \"30s\"     # Default: 30s" "$ACTION_URL" "$(echo "$ACTION_METHOD" | tr '[:lower:]' '[:upper:]')" "$headers")
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
