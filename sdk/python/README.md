# mtix Python SDK

Python client for the mtix micro-issue manager. Provides a Pythonic interface to all mtix operations via the REST API.

## Installation

```bash
pip install -e sdk/python/
```

## Quick Start

```python
from mtix import MtixClient

client = MtixClient()  # Connects to localhost:6849

# Create a task
node = client.micro("Implement login page", project="AUTH")

# Claim and work
client.claim(node.id, agent="my-agent")
ctx = client.context(node.id)
print(ctx.assembled_prompt)

# Complete
client.done(node.id, agent="my-agent")
```

## API Reference

See the `MtixClient` class docstrings for complete method documentation.
