import base64
import json
import sys
from datetime import datetime

import requests

API_URL = "http://127.0.0.1:8317/v1/chat/completions"
API_KEY = "test-key-123"


def generate_image(
    prompt: str, model: str = "gemini-3-pro-image-preview-4k", output_file: str = None
):
    """Generate an image using the CLIProxyAPI and save it to a file."""

    headers = {"Content-Type": "application/json", "Authorization": f"Bearer {API_KEY}"}

    payload = {"model": model, "messages": [{"role": "user", "content": prompt}]}

    print(f"Generating image with model: {model}")
    print(f"Prompt: {prompt}")
    print("Waiting for response...")

    response = requests.post(API_URL, headers=headers, json=payload, timeout=120)

    if response.status_code != 200:
        print(f"Error: {response.status_code} - {response.text}")
        return None

    data = response.json()

    # Debug: print response structure
    print(f"Response keys: {data.keys()}")

    if "choices" not in data or len(data["choices"]) == 0:
        print("Error: No choices in response")
        print(f"Full response: {json.dumps(data, indent=2)[:1000]}")
        return None

    choice = data["choices"][0]
    print(f"Choice keys: {choice.keys()}")

    # Try different content locations
    content = None
    if "message" in choice:
        msg = choice["message"]
        print(f"Message keys: {msg.keys()}")
        content = msg.get("content", "")
    elif "delta" in choice:
        content = choice["delta"].get("content", "")

    # Check for base64 content in different formats
    if not content and "message" in choice:
        msg = choice["message"]
        # Check for images array
        if "images" in msg and msg["images"]:
            img = msg["images"][0]  # Take first image
            if isinstance(img, dict):
                print(f"Found image dict with keys: {img.keys()}")
                # Check for nested image_url structure
                if "image_url" in img:
                    url_data = img["image_url"]
                    if isinstance(url_data, dict):
                        content = url_data.get("url", "")
                    else:
                        content = url_data
                else:
                    content = (
                        img.get("data", "")
                        or img.get("b64_json", "")
                        or img.get("url", "")
                    )
            else:
                content = img
            print(f"Found image in 'images' field")
        # Check for parts array (Gemini format)
        elif "parts" in msg:
            for part in msg["parts"]:
                if "inline_data" in part:
                    content = part["inline_data"].get("data", "")
                    break

    if not content:
        print("Error: No content in response")
        print(f"Message: {json.dumps(choice.get('message', {}), indent=2)[:500]}")
        return None

    # Check if content is base64 image data or data URL
    if content:
        # Handle data URL format (data:image/jpeg;base64,...)
        if content.startswith("data:image"):
            # Extract base64 part
            parts = content.split(",", 1)
            if len(parts) == 2:
                content = parts[1]
                # Determine format from mime type
                if "jpeg" in parts[0] or "jpg" in parts[0]:
                    ext = "jpg"
                elif "png" in parts[0]:
                    ext = "png"
                else:
                    ext = "jpg"
            else:
                print("Error: Invalid data URL format")
                return None
        elif content.startswith("/9j/"):
            ext = "jpg"
        elif content.startswith("iVBOR"):
            ext = "png"
        else:
            print("Response content (text):")
            print(content[:500] + "..." if len(content) > 500 else content)
            return None

        image_data = base64.b64decode(content)

        if not output_file:
            timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
            output_file = f"generated_image_{timestamp}.{ext}"

        with open(output_file, "wb") as f:
            f.write(image_data)

        print(f"Image saved to: {output_file}")
        return output_file
    else:
        print("Error: No image content found")
        return None


if __name__ == "__main__":
    # Default prompt
    prompt = "A high mountain rising from a vast expanse of white sand desert, extremely hot and desolate landscape, no living trees, only dead withered trees scattered everywhere, harsh sunlight, barren wasteland, dramatic scenery, photorealistic"

    if len(sys.argv) > 1:
        prompt = " ".join(sys.argv[1:])

    generate_image(prompt)
