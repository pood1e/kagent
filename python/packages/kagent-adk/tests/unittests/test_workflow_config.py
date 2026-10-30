import sys
from unittest.mock import MagicMock

import pytest


@pytest.fixture(autouse=True)
def _isolate_types_import(monkeypatch):
    for mod_name in ["kagent.core", "kagent.core.a2a", "kagent.core.telemetry", "agentsts", "agentsts.adk"]:
        if mod_name not in sys.modules:
            monkeypatch.setitem(sys.modules, mod_name, MagicMock())


def test_create_agent_rejects_declarative_workflow():
    import kagent.adk.types as types_mod

    config = types_mod.AgentConfig(
        model={"model": "gemini-3.5-flash", "gemini": {}},
        workflow={"document": b"{}", "sha256": "0" * 64},
    )
    with pytest.raises(ValueError, match="Go runtime"):
        types_mod.create_agent(config, "workflow")
