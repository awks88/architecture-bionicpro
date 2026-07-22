CREATE TABLE clients (
    client_id BIGINT PRIMARY KEY,
    user_id TEXT UNIQUE NOT NULL,
    full_name TEXT NOT NULL,
    prosthesis_model TEXT NOT NULL,
    crm_status TEXT NOT NULL
);

INSERT INTO clients (client_id, user_id, full_name, prosthesis_model, crm_status) VALUES
    (1, 'john.doe', 'John Doe', 'Bionic Arm M2', 'active'),
    (2, 'jane.smith', 'Jane Smith', 'Bionic Hand H1', 'active'),
    (3, 'alex.johnson', 'Alex Johnson', 'Bionic Leg L3', 'service');
