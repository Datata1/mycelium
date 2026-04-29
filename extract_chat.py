import json
import sys

def parse_claude_log(input_file, output_file):
    with open(input_file, 'r', encoding='utf-8') as infile, \
         open(output_file, 'w', encoding='utf-8') as outfile:
        
        for line_num, line in enumerate(infile):
            if not line.strip():
                continue
            
            try:
                data = json.loads(line)
            except json.JSONDecodeError:
                continue

            # Rekursive Funktion, um die relevanten Blöcke in der JSON-Struktur zu finden
            def extract_content(obj):
                if isinstance(obj, dict):
                    # Ist es ein normaler Text-Block?
                    if obj.get("type") == "text" and "text" in obj:
                        outfile.write(f"\n💬 **Message:**\n{obj['text']}\n")
                    
                    # Ist es ein Tool-Aufruf?
                    elif obj.get("type") == "tool_use":
                        tool_name = obj.get("name", "unknown_tool")
                        tool_input = obj.get("input", {})
                        outfile.write(f"\n🛠️ **Tool Call ({tool_name}):**\n```json\n{json.dumps(tool_input, indent=2)}\n```\n")
                    
                    # Ist es das Ergebnis eines Tools?
                    elif obj.get("type") == "tool_result":
                        content = obj.get("content", "")
                        # Manchmal ist der Content ein String, manchmal ein Array
                        if isinstance(content, list):
                            content_str = "\n".join([c.get("text", "") for c in content if isinstance(c, dict) and "text" in c])
                        else:
                            content_str = str(content)
                        
                        outfile.write(f"\n✅ **Tool Result:**\n```\n{content_str}\n```\n")
                    
                    # Weiter tiefersuchen
                    for value in obj.values():
                        extract_content(value)
                
                elif isinstance(obj, list):
                    for item in obj:
                        extract_content(item)

            extract_content(data)
            
    print(f"Fertig! Die bereinigte Datei liegt hier: {output_file}")

if __name__ == "__main__":
    if len(sys.argv) != 3:
        print("Nutzung: python extract_chat.py <input.jsonl> <output.md>")
        sys.exit(1)
    
    parse_claude_log(sys.argv[1], sys.argv[2])