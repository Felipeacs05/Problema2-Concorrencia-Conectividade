# Guia de Contribuição

Obrigado por considerar contribuir para este projeto! Este documento fornece diretrizes para contribuições.

## Como Contribuir

### Reportando Bugs

Antes de reportar um bug, verifique se já não existe uma issue aberta sobre o problema.

Ao criar uma issue de bug, inclua:
- Descrição clara do problema
- Passos para reproduzir
- Comportamento esperado vs comportamento atual
- Ambiente (OS, versão do Go, versão do Docker)
- Logs relevantes

### Sugerindo Melhorias

Issues de melhoria são bem-vindas! Inclua:
- Descrição clara da funcionalidade
- Justificativa (por que seria útil)
- Exemplos de uso, se possível

### Pull Requests

1. **Fork** o repositório
2. **Crie uma branch** para sua feature: `git checkout -b feature/minha-feature`
3. **Faça commits** significativos: `git commit -m 'feat: adiciona funcionalidade X'`
4. **Push** para sua branch: `git push origin feature/minha-feature`
5. **Abra um Pull Request** descrevendo suas alterações

### Padrões de Código

#### Commits
Usamos [Conventional Commits](https://www.conventionalcommits.org/):

```
<tipo>(<escopo>): <descrição>

[corpo opcional]

[rodapé opcional]
```

**Tipos**:
- `feat`: Nova funcionalidade
- `fix`: Correção de bug
- `docs`: Documentação
- `style`: Formatação
- `refactor`: Refatoração
- `test`: Testes
- `chore`: Manutenção

**Exemplos**:
```
feat(servidor): adiciona endpoint de métricas
fix(cliente): corrige reconexão MQTT
docs: atualiza README com novos comandos
test(servidor): adiciona testes de eleição de líder
```

#### Código Go

- Siga as convenções do [Effective Go](https://golang.org/doc/effective_go.html)
- Use `gofmt` para formatação
- Use `golint` para verificação de estilo
- Mantenha funções pequenas e focadas
- Adicione comentários para código complexo
- Escreva testes para novas funcionalidades

#### Documentação

- Atualize o README se adicionar/modificar funcionalidades
- Documente funções públicas com comentários
- Mantenha o CHANGELOG atualizado

## Processo de Review

1. Mantedores revisarão o PR
2. Podem solicitar alterações
3. Testes automatizados devem passar
4. Após aprovação, será feito merge

## Código de Conduta

- Seja respeitoso e profissional
- Aceite críticas construtivas
- Foque no que é melhor para o projeto

## Dúvidas?

Abra uma issue com a tag `question` ou entre em contato com os mantedores.

---

Obrigado por contribuir! 🎉

