-- =============================================================================
-- Aula 1 · §3 — O ledger como decisão de System Design.
--
--   "Log é verdade, saldo é projeção."
--
-- Este schema não tem uma coluna `saldo` em lugar nenhum do write model.
-- Saldo é SEMPRE derivado de `entries`. Isso é uma decisão, não um detalhe:
--   1. sem auditoria      -> UPDATE em saldo sabe o "agora", não o "como chegou"
--   2. lost update        -> duas escritas concorrentes criam/destroem dinheiro
--   3. não reconstruível  -> se corromper, a verdade se perdeu
-- =============================================================================

-- -----------------------------------------------------------------------------
-- Contas contábeis: "potes" de valor com natureza.
-- ATENÇÃO ao conceito que mais confunde: o saldo do cliente é PASSIVO da fintech.
-- Aquele dinheiro não é nosso — nós DEVEMOS ele ao cliente.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS accounts (
    id             uuid        PRIMARY KEY,
    code           text        NOT NULL UNIQUE,
    name           text        NOT NULL,
    kind           text        NOT NULL CHECK (kind IN ('ATIVO', 'PASSIVO')),
    -- Invariante de domínio: quase nenhuma conta pode ficar negativa.
    -- A exceção é patrimônio/resultado, que por natureza "financia" o resto.
    allow_negative boolean     NOT NULL DEFAULT false,
    owner_name     text,
    owner_tax_id   text,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- Chaves Pix DESTE participante. O diretório oficial (DICT) vive no BACEN;
-- aqui guardamos só o que é nosso. Nunca somos a fonte da verdade do DICT.
CREATE TABLE IF NOT EXISTS pix_keys (
    key        text        PRIMARY KEY,
    key_type   text        NOT NULL CHECK (key_type IN ('CPF','CNPJ','TELEFONE','EMAIL','EVP')),
    account_id uuid        NOT NULL REFERENCES accounts (id),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- -----------------------------------------------------------------------------
-- Transação: conjunto BALANCEADO de lançamentos = um fato econômico.
-- Um Pix gera DUAS transações (reserva e liquidação), ambas com o mesmo E2E ID.
-- Daí a unicidade ser (e2e_id, kind) e não só (e2e_id).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS transactions (
    id          uuid        PRIMARY KEY,
    e2e_id      text,
    kind        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Regra do BACEN, virada constraint: o E2E ID é único.
-- "Pagamento fantasma foi proibido por desenho regulatório." (§4.4)
CREATE UNIQUE INDEX IF NOT EXISTS transactions_e2e_kind_uniq
    ON transactions (e2e_id, kind) WHERE e2e_id IS NOT NULL;

-- -----------------------------------------------------------------------------
-- Lançamento: um débito OU um crédito. Nunca existe sozinho.
-- Este é o log append-only. É a fonte da verdade do sistema inteiro.
-- -----------------------------------------------------------------------------
CREATE SEQUENCE IF NOT EXISTS entries_id_seq;

CREATE TABLE IF NOT EXISTS entries (
    id             bigint      PRIMARY KEY DEFAULT nextval('entries_id_seq'),
    transaction_id uuid        NOT NULL REFERENCES transactions (id),
    account_id     uuid        NOT NULL REFERENCES accounts (id),
    direction      char(1)     NOT NULL CHECK (direction IN ('D', 'C')),
    -- Dinheiro é inteiro em centavos. Float em dinheiro é bug esperando acontecer.
    amount_cents   bigint      NOT NULL CHECK (amount_cents > 0),
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS entries_account_idx ON entries (account_id, id);
CREATE INDEX IF NOT EXISTS entries_tx_idx      ON entries (transaction_id);
CREATE INDEX IF NOT EXISTS entries_time_idx    ON entries (created_at);

-- -----------------------------------------------------------------------------
-- IMUTABILIDADE, no banco e não só no código.
-- O log é append-only. Nem a aplicação, nem o DBA de plantão às 3h da manhã
-- conseguem "só ajustar uma linhazinha".
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION deny_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'append-only: % em % nao e permitido (ADR-001)', TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS entries_append_only ON entries;
CREATE TRIGGER entries_append_only
    BEFORE UPDATE OR DELETE ON entries
    FOR EACH ROW EXECUTE FUNCTION deny_mutation();

DROP TRIGGER IF EXISTS transactions_append_only ON transactions;
CREATE TRIGGER transactions_append_only
    BEFORE UPDATE OR DELETE ON transactions
    FOR EACH ROW EXECUTE FUNCTION deny_mutation();

-- -----------------------------------------------------------------------------
-- FITNESS FUNCTION #1, executável, dentro do banco:
--     em toda transação, Σ débitos = Σ créditos.
--
-- DEFERRABLE INITIALLY DEFERRED: só cobra no COMMIT, porque durante a transação
-- o par ainda está sendo montado. É a invariante do domínio virando guardrail
-- (§7.4 — o Harness precisa ser projetado DENTRO do sistema).
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION assert_double_entry() RETURNS trigger AS $$
DECLARE
    debitos  bigint;
    creditos bigint;
BEGIN
    SELECT COALESCE(SUM(amount_cents) FILTER (WHERE direction = 'D'), 0),
           COALESCE(SUM(amount_cents) FILTER (WHERE direction = 'C'), 0)
      INTO debitos, creditos
      FROM entries
     WHERE transaction_id = NEW.transaction_id;

    IF debitos <> creditos THEN
        RAISE EXCEPTION 'partida dobrada violada na transacao %: debitos=% creditos=%',
            NEW.transaction_id, debitos, creditos
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS entries_double_entry ON entries;
CREATE CONSTRAINT TRIGGER entries_double_entry
    AFTER INSERT ON entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION assert_double_entry();

-- -----------------------------------------------------------------------------
-- Saldo forte: função pura sobre o log. Não é cache, não é coluna. É soma.
-- Convenção de sinal: PASSIVO tem saldo credor (C - D); ATIVO tem saldo devedor.
-- Assim "saldo >= 0" significa a mesma coisa nas duas naturezas.
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION account_balance(p_account_id uuid) RETURNS bigint AS $$
    SELECT COALESCE(SUM(
               CASE WHEN e.direction = 'C' THEN e.amount_cents ELSE -e.amount_cents END
           ), 0) * CASE WHEN a.kind = 'ATIVO' THEN -1 ELSE 1 END
      FROM accounts a
      LEFT JOIN entries e ON e.account_id = a.id
     WHERE a.id = p_account_id
     GROUP BY a.kind;
$$ LANGUAGE sql STABLE;

-- =============================================================================
-- §4 — Idempotência: correção sob incerteza.
--
-- A chave identifica a INTENÇÃO (os três toques da Ana), não a tentativa.
-- O registro tem ESTADO (em andamento / concluído), porque o retry concorrente
-- chega ANTES do primeiro terminar. E o "concluído" é gravado na MESMA
-- transação que os lançamentos — ou tudo, ou nada.
-- =============================================================================
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key             text        PRIMARY KEY,
    scope           text        NOT NULL,
    -- Mesma chave com payload diferente = erro do cliente, não um replay.
    request_hash    text        NOT NULL,
    state           text        NOT NULL CHECK (state IN ('IN_PROGRESS', 'DONE', 'FAILED')),
    response_status int,
    response_body   jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idempotency_state_idx ON idempotency_keys (state, updated_at);

-- =============================================================================
-- §6.5 — Máquina de estados de um Pix. O ledger guarda o DINHEIRO;
-- esta tabela guarda o PROCESSO (que pode ficar preso no meio do caminho).
-- Estado explícito no log => retomável pela reconciliação (§4.5).
-- =============================================================================
CREATE TABLE IF NOT EXISTS pix_payments (
    e2e_id             text        PRIMARY KEY,
    payer_account_id   uuid        NOT NULL REFERENCES accounts (id),
    payer_account_code text        NOT NULL,
    payee_key          text        NOT NULL,
    payee_ispb         text        NOT NULL,
    payee_bank         text        NOT NULL DEFAULT '',
    payee_name         text        NOT NULL DEFAULT '',
    amount_cents       bigint      NOT NULL CHECK (amount_cents > 0),
    description        text        NOT NULL DEFAULT '',
    -- Três estados, não quatro: uma rejeição do SPI não é um estado de repouso,
    -- é o gatilho de um estorno. O pagamento termina em SETTLED ou REFUNDED.
    status             text        NOT NULL CHECK (status IN ('RESERVED', 'SETTLED', 'REFUNDED')),
    spi_status         text,
    spi_reason         text,
    reserve_tx_id      uuid,
    settle_tx_id       uuid,
    budget_json        jsonb,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS pix_payments_status_idx ON pix_payments (status, created_at);

-- =============================================================================
-- §5.5 — Borda eventual. CQRS não é luxo: leitura >> escrita.
-- Estas tabelas são PROJEÇÃO. Se apagarem todas, o sistema reconstrói do log.
-- Se apagarem `entries`, acabou o sistema. Essa assimetria é o ponto.
-- =============================================================================
CREATE TABLE IF NOT EXISTS balances_projection (
    account_id    uuid        PRIMARY KEY REFERENCES accounts (id),
    balance_cents bigint      NOT NULL DEFAULT 0,
    last_entry_id bigint      NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS statement_entries (
    entry_id            bigint      PRIMARY KEY,
    account_id          uuid        NOT NULL,
    transaction_id      uuid        NOT NULL,
    e2e_id              text,
    kind                text        NOT NULL,
    description         text        NOT NULL DEFAULT '',
    direction           char(1)     NOT NULL,
    amount_cents        bigint      NOT NULL,
    signed_cents        bigint      NOT NULL,
    balance_after_cents bigint      NOT NULL,
    occurred_at         timestamptz NOT NULL,
    projected_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS statement_account_idx ON statement_entries (account_id, entry_id DESC);

CREATE TABLE IF NOT EXISTS projection_cursor (
    name          text        PRIMARY KEY,
    last_entry_id bigint      NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
