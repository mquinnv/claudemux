// The phone-side page: enter the lobby token once, see the last log lines.

export type SetupDeps = {
  origin: string; // baked in at pack time; empty for an unpacked dev build
  hasToken: boolean;
  saveToken: (token: string) => Promise<void>;
};

export function mountSetup(root: HTMLElement, deps: SetupDeps): void {
  root.textContent = '';
  const origin = document.createElement('p');
  origin.textContent = deps.origin
    ? `Lobby: ${deps.origin}`
    : 'Built without CLAUDEMUX_REMOTE_ORIGIN: the app cannot reach a lobby.';
  const label = document.createElement('label');
  label.textContent = 'Remote token (CLAUDEMUX_REMOTE_TOKEN)';
  const input = document.createElement('input');
  input.type = 'password';
  input.autocomplete = 'off';
  input.placeholder = deps.hasToken ? 'saved - enter a new one to replace it' : 'paste the token';
  label.append(input);
  const button = document.createElement('button');
  button.textContent = 'Save token';
  const status = document.createElement('div');
  status.id = 'status';
  button.addEventListener('click', () => {
    const token = input.value.trim();
    if (token === '') {
      status.textContent = 'enter a token first';
      return;
    }
    deps.saveToken(token).then(
      () => {
        input.value = '';
        status.textContent = 'saved';
      },
      () => {
        status.textContent = 'could not save the token';
      },
    );
  });
  root.append(origin, label, button, status);
}

export function showLog(el: HTMLElement, lines: string[]): void {
  el.textContent = lines.join('\n');
}
