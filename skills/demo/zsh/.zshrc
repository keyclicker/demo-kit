# zsh for demo terminals (ghostty.conf): a short prompt, the user's
# syntax highlighting, and no history, so nothing personal is shown

unset HISTFILE
HISTSIZE=0

# The user's plugins, if they keep them here: highlighting; with no
# history, autosuggestions have nothing to suggest
[[ -r ~/.config/zsh/plugins.zsh ]] && source ~/.config/zsh/plugins.zsh

PROMPT='%F{blue}%1~%f %F{green}$%f '
