
@id("default-endpoint-access")
permit (
  principal,
  action in [Action::"endpoint:access", Action::"http:request"],
  resource
);

@id("default-agent-connect")
permit (
  principal,
  action == Action::"agent:connect",
  resource
)
when { resource.connect_any_agent || resource.connect_agents.contains(context.agent) };
