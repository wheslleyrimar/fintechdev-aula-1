-- =============================================================================
-- Plano de contas mínimo do TechPix + capital inicial.
-- Repare no espelho da §6.3: nós não temos dinheiro "circulando".
-- Temos saldo na Conta PI dentro do BACEN (reserva_no_bc) e DEVEMOS
-- esse dinheiro aos clientes (carteira:*). O ledger espelha a Conta PI.
-- =============================================================================

INSERT INTO accounts (id, code, name, kind, allow_negative, owner_name, owner_tax_id) VALUES
    ('00000000-0000-4000-8000-000000000001', 'reserva_no_bc',     'Conta PI no BACEN (reservas)',  'ATIVO',   false, NULL, NULL),
    ('00000000-0000-4000-8000-000000000002', 'pix_a_liquidar',    'Pix reservado, a liquidar',     'PASSIVO', false, NULL, NULL),
    ('00000000-0000-4000-8000-000000000003', 'patrimonio:capital','Capital do TechPix',            'PASSIVO', true,  NULL, NULL),
    ('00000000-0000-4000-8000-000000000004', 'receita:tarifas',   'Receita de tarifas',            'PASSIVO', true,  NULL, NULL),
    ('00000000-0000-4000-8000-000000000010', 'carteira:ana',      'Carteira de Ana Souza',         'PASSIVO', false, 'Ana Souza',    '11144477735'),
    ('00000000-0000-4000-8000-000000000011', 'carteira:joao',     'Carteira de Joao Lima',         'PASSIVO', false, 'Joao Lima',    '52998224725'),
    ('00000000-0000-4000-8000-000000000012', 'carteira:carla',    'Carteira de Carla Dias',        'PASSIVO', false, 'Carla Dias',   '39053344705')
ON CONFLICT (code) DO NOTHING;

-- CPFs aqui sao validos no digito verificador de proposito: o cliente do DICT
-- valida modulo 11 ANTES de consultar, para nao queimar 20 tokens num 404.
INSERT INTO pix_keys (key, key_type, account_id) VALUES
    ('+5511999990001',      'TELEFONE', '00000000-0000-4000-8000-000000000010'),
    ('joao@techpix.com.br', 'EMAIL',    '00000000-0000-4000-8000-000000000011'),
    ('39053344705',         'CPF',      '00000000-0000-4000-8000-000000000012')
ON CONFLICT (key) DO NOTHING;

-- Fato 0: capitalização. R$ 1.000.000,00 entram na Conta PI.
--   DÉBITO reserva_no_bc (ativo sobe)  |  CRÉDITO patrimonio:capital
INSERT INTO transactions (id, e2e_id, kind, description) VALUES
    ('00000000-0000-4000-9000-000000000001', NULL, 'capitalizacao', 'Aporte inicial na Conta PI')
ON CONFLICT (id) DO NOTHING;

INSERT INTO entries (transaction_id, account_id, direction, amount_cents)
SELECT '00000000-0000-4000-9000-000000000001', '00000000-0000-4000-8000-000000000001', 'D', 100000000
WHERE NOT EXISTS (SELECT 1 FROM entries WHERE transaction_id = '00000000-0000-4000-9000-000000000001');

INSERT INTO entries (transaction_id, account_id, direction, amount_cents)
SELECT '00000000-0000-4000-9000-000000000001', '00000000-0000-4000-8000-000000000003', 'C', 100000000
WHERE NOT EXISTS (
    SELECT 1 FROM entries
     WHERE transaction_id = '00000000-0000-4000-9000-000000000001' AND direction = 'C'
);

-- Fato 1: Ana deposita R$ 10.000,00 (cash-in).
--   DÉBITO reserva_no_bc  |  CRÉDITO carteira:ana (passivo sobe: devemos a ela)
INSERT INTO transactions (id, e2e_id, kind, description) VALUES
    ('00000000-0000-4000-9000-000000000002', NULL, 'deposito', 'Aporte inicial da conta de Ana')
ON CONFLICT (id) DO NOTHING;

INSERT INTO entries (transaction_id, account_id, direction, amount_cents)
SELECT '00000000-0000-4000-9000-000000000002', '00000000-0000-4000-8000-000000000001', 'D', 1000000
WHERE NOT EXISTS (SELECT 1 FROM entries WHERE transaction_id = '00000000-0000-4000-9000-000000000002');

INSERT INTO entries (transaction_id, account_id, direction, amount_cents)
SELECT '00000000-0000-4000-9000-000000000002', '00000000-0000-4000-8000-000000000010', 'C', 1000000
WHERE NOT EXISTS (
    SELECT 1 FROM entries
     WHERE transaction_id = '00000000-0000-4000-9000-000000000002' AND direction = 'C'
);

INSERT INTO projection_cursor (name, last_entry_id) VALUES ('statement', 0)
ON CONFLICT (name) DO NOTHING;
