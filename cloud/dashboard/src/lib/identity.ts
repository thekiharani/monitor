interface Person {
  email: string;
  display_name?: string;
}

interface Workspace {
  name?: string;
  plan: string;
  role: string;
}

export function personName(person: Person): string {
  return person.display_name?.trim() || person.email;
}

export function personInitials(person: Person): string {
  const source = person.display_name?.trim() || person.email;
  const words = source.split(/[\s@._-]+/).filter(Boolean);
  const letters = words.slice(0, 2).map((word) => word[0]);
  return letters.join("").toUpperCase() || "?";
}

export function workspaceName(workspace: Workspace): string {
  return workspace.name?.trim() || `${workspace.plan} · ${workspace.role}`;
}

export function workspaceInitial(workspace: Workspace): string {
  const trimmed = workspace.name?.trim();
  return (trimmed || workspace.plan)[0]?.toUpperCase() ?? "?";
}
