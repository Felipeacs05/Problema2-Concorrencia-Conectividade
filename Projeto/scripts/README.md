# Scripts de Utilidade

Este diretório contém scripts úteis para desenvolvimento, teste e build do projeto.

## Scripts Disponíveis

### build.sh
Compila o projeto gerando binários otimizados.

```bash
chmod +x scripts/build.sh
./scripts/build.sh
```

Saída: `bin/servidor` e `bin/cliente`

### test.sh
Executa todos os testes do projeto.

```bash
chmod +x scripts/test.sh
./scripts/test.sh
```

Inclui:
- Verificação de compilação
- Testes unitários com cobertura
- Benchmarks de performance
- Verificação de formatação

### clean.sh
Limpa arquivos temporários e binários.

```bash
chmod +x scripts/clean.sh
./scripts/clean.sh
```

Remove:
- Binários compilados
- Arquivos de teste
- Logs
- Dados temporários

## Uso no Windows

No Windows com Git Bash:
```bash
bash scripts/build.sh
bash scripts/test.sh
bash scripts/clean.sh
```

Ou use o Makefile que funciona em todas as plataformas:
```bash
make test-build
make test-unit
make clean
```

